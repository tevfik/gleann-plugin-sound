// Package audio provides OS-level audio capture, voice activity detection,
// and sample-rate conversion utilities for gleann-sound.
//
// All output is normalised to 16 kHz, 16-bit, Mono PCM — the only format
// accepted by Whisper.
package audio

import (
	"context"
	"encoding/binary"
	"fmt"
	"log"
	"runtime"
	"strings"
	"sync"

	"github.com/gen2brain/malgo"
	"github.com/tevfik/gleann-sound/internal/core"
)

// ---------------------------------------------------------------------------
// Constants
// ---------------------------------------------------------------------------

const (
	// WhisperSampleRate is the only sample rate Whisper accepts.
	WhisperSampleRate = 16000
	// WhisperChannels – mono.
	WhisperChannels = 1
	// WhisperBitsPerSample – signed 16-bit PCM.
	WhisperBitsPerSample = 16
	// captureFrameSize defines how many frames malgo delivers per callback.
	// Using 480 frames at 16 kHz ≈ 30 ms per chunk which is a good trade-off
	// between latency and overhead.
	captureFrameSize = 480
)

// ---------------------------------------------------------------------------
// AudioSource — what to capture
// ---------------------------------------------------------------------------

// AudioSource controls which audio device(s) the capturer listens to.
type AudioSource int

const (
	// SourceMic captures from the default microphone input (default).
	SourceMic AudioSource = iota
	// SourceSpeaker captures system/desktop audio output (loopback).
	// Platform support:
	//   - Linux: uses PulseAudio monitor source
	//   - Windows: uses WASAPI loopback
	//   - macOS: requires a virtual audio device (e.g. BlackHole)
	SourceSpeaker
	// SourceBoth captures from both microphone and speaker simultaneously,
	// mixing the two streams into one.
	SourceBoth
)

// ParseAudioSource converts a string to an AudioSource.
// Valid values: "mic", "speaker", "both".
func ParseAudioSource(s string) (AudioSource, error) {
	switch s {
	case "mic", "microphone":
		return SourceMic, nil
	case "speaker", "loopback", "desktop":
		return SourceSpeaker, nil
	case "both", "all":
		return SourceBoth, nil
	default:
		return SourceMic, fmt.Errorf("unknown audio source %q (valid: mic, speaker, both)", s)
	}
}

// String returns the string representation of an AudioSource.
func (s AudioSource) String() string {
	switch s {
	case SourceMic:
		return "mic"
	case SourceSpeaker:
		return "speaker"
	case SourceBoth:
		return "both"
	}
	return "mic"
}

// ---------------------------------------------------------------------------
// MalgoCapturer
// ---------------------------------------------------------------------------

// MalgoCapturer implements core.AudioCapturer using the MiniAudio library
// (via malgo) for cross-platform audio input.
//
// It can capture from the microphone, system audio (loopback), or both
// simultaneously depending on the configured AudioSource.
type MalgoCapturer struct {
	mu      sync.Mutex
	source  AudioSource
	devices []*malgoDevice // one or two active devices
	running bool
}

// malgoDevice holds a single malgo context + device pair.
type malgoDevice struct {
	ctx    *malgo.AllocatedContext
	device *malgo.Device
	label  string
}

// Compile-time interface check.
var _ core.AudioCapturer = (*MalgoCapturer)(nil)

// NewMalgoCapturer creates a capturer that records from the default microphone.
func NewMalgoCapturer() *MalgoCapturer {
	return &MalgoCapturer{source: SourceMic}
}

// NewMalgoCapturerWithSource creates a capturer for the given audio source.
func NewMalgoCapturerWithSource(src AudioSource) *MalgoCapturer {
	return &MalgoCapturer{source: src}
}

// initMalgoContext creates a malgo context with the preferred backend order.
func initMalgoContext() (*malgo.AllocatedContext, error) {
	backends := []malgo.Backend{
		malgo.BackendPulseaudio,
		malgo.BackendAlsa,
		malgo.BackendWasapi,
		malgo.BackendCoreaudio,
	}
	return malgo.InitContext(backends, malgo.ContextConfig{}, nil)
}

// startCaptureDevice initialises and starts a single capture device.
// If deviceID is non-nil, it selects that specific device instead of the default.
func startCaptureDevice(devType malgo.DeviceType, deviceID *malgo.DeviceID, onData func([]int16), label string) (*malgoDevice, error) {
	mctx, err := initMalgoContext()
	if err != nil {
		return nil, fmt.Errorf("audio(%s): failed to init malgo context: %w", label, err)
	}

	deviceConfig := malgo.DefaultDeviceConfig(devType)
	deviceConfig.Capture.Format = malgo.FormatS16
	deviceConfig.Capture.Channels = WhisperChannels
	deviceConfig.SampleRate = WhisperSampleRate
	deviceConfig.PeriodSizeInFrames = captureFrameSize
	deviceConfig.Alsa.NoMMap = 1

	// Set specific device if requested.
	if deviceID != nil {
		deviceConfig.Capture.DeviceID = deviceID.Pointer()
	}

	onRecvFrames := func(outputSamples, inputSamples []byte, framecount uint32) {
		sampleCount := len(inputSamples) / 2
		if sampleCount == 0 {
			return
		}
		pcm := make([]int16, sampleCount)
		for i := 0; i < sampleCount; i++ {
			pcm[i] = int16(binary.LittleEndian.Uint16(inputSamples[i*2 : i*2+2]))
		}
		onData(pcm)
	}

	dev, err := malgo.InitDevice(mctx.Context, deviceConfig, malgo.DeviceCallbacks{Data: onRecvFrames})
	if err != nil {
		mctx.Uninit()
		mctx.Free()
		return nil, fmt.Errorf("audio(%s): failed to init device: %w", label, err)
	}

	if err := dev.Start(); err != nil {
		dev.Uninit()
		mctx.Uninit()
		mctx.Free()
		return nil, fmt.Errorf("audio(%s): failed to start device: %w", label, err)
	}

	log.Printf("[audio] %s capture started — 16 kHz / 16-bit / mono", label)
	return &malgoDevice{ctx: mctx, device: dev, label: label}, nil
}

// findMonitorDevice enumerates capture devices and returns the best
// PulseAudio/PipeWire monitor source for loopback capture.
//
// Strategy: find the default playback device name, then look for a monitor
// source that corresponds to it (e.g. "Monitor of <playback-device-name>").
// Falls back to the first monitor source if no match is found.
func findMonitorDevice() (*malgo.DeviceID, string, error) {
	mctx, err := initMalgoContext()
	if err != nil {
		return nil, "", fmt.Errorf("failed to init malgo context for device enumeration: %w", err)
	}
	defer func() {
		mctx.Uninit()
		mctx.Free()
	}()

	// Find the default playback device name.
	var defaultPlaybackName string
	playbackDevices, err := mctx.Context.Devices(malgo.Playback)
	if err == nil {
		for _, d := range playbackDevices {
			if d.IsDefault != 0 {
				defaultPlaybackName = d.Name()
				log.Printf("[audio] default playback device: %s", defaultPlaybackName)
				break
			}
		}
	}

	// Enumerate capture devices and find monitor sources.
	captureDevices, err := mctx.Context.Devices(malgo.Capture)
	if err != nil {
		return nil, "", fmt.Errorf("failed to enumerate capture devices: %w", err)
	}

	var monitors []malgo.DeviceInfo
	for _, d := range captureDevices {
		name := d.Name()
		if strings.Contains(strings.ToLower(name), "monitor") {
			monitors = append(monitors, d)
		}
	}

	if len(monitors) == 0 {
		return nil, "", fmt.Errorf("no PulseAudio/PipeWire monitor source found — " +
			"ensure PulseAudio or PipeWire (pipewire-pulse) is running")
	}

	// Prefer the monitor that matches the default playback device.
	if defaultPlaybackName != "" {
		for _, d := range monitors {
			name := d.Name()
			// Monitor names follow the pattern: "Monitor of <playback-device-name>"
			if strings.Contains(name, defaultPlaybackName) {
				id := d.ID
				log.Printf("[audio] found monitor device (default match): %s", name)
				return &id, name, nil
			}
		}
	}

	// Fall back to the first monitor source.
	id := monitors[0].ID
	name := monitors[0].Name()
	log.Printf("[audio] found monitor device (fallback): %s", name)
	return &id, name, nil
}

// startSpeakerCapture starts loopback/desktop audio capture.
//
// Platform strategy:
//   - Windows (WASAPI): native malgo.Loopback device type
//   - Linux (PulseAudio/PipeWire): find monitor source via device enumeration
//   - macOS: no native loopback — requires virtual audio device (e.g. BlackHole)
//     which appears as a regular capture device with "BlackHole" in the name
func startSpeakerCapture(onData func([]int16)) (*malgoDevice, error) {
	switch runtime.GOOS {
	case "windows":
		// WASAPI has native loopback support.
		return startCaptureDevice(malgo.Loopback, nil, onData, "speaker")

	case "linux":
		// Find PulseAudio/PipeWire monitor source.
		devID, name, err := findMonitorDevice()
		if err != nil {
			return nil, fmt.Errorf("audio(speaker): %w", err)
		}
		log.Printf("[audio] using monitor source: %s", name)
		return startCaptureDevice(malgo.Capture, devID, onData, "speaker")

	case "darwin":
		// macOS has no native loopback.
		// Try to find a virtual audio device (BlackHole, Loopback, etc.)
		devID, name, err := findVirtualAudioDevice()
		if err != nil {
			return nil, fmt.Errorf("audio(speaker): macOS requires a virtual audio device "+
				"(e.g. BlackHole: https://github.com/ExistentialAudio/BlackHole) "+
				"for speaker capture: %w", err)
		}
		log.Printf("[audio] using virtual audio device: %s", name)
		return startCaptureDevice(malgo.Capture, devID, onData, "speaker")

	default:
		return nil, fmt.Errorf("audio(speaker): loopback capture not supported on %s", runtime.GOOS)
	}
}

// findVirtualAudioDevice looks for virtual audio devices (BlackHole, Loopback,
// Soundflower) that provide loopback on macOS.
func findVirtualAudioDevice() (*malgo.DeviceID, string, error) {
	mctx, err := initMalgoContext()
	if err != nil {
		return nil, "", fmt.Errorf("failed to init malgo context: %w", err)
	}
	defer func() {
		mctx.Uninit()
		mctx.Free()
	}()

	devices, err := mctx.Context.Devices(malgo.Capture)
	if err != nil {
		return nil, "", fmt.Errorf("failed to enumerate capture devices: %w", err)
	}

	// Known virtual audio device names on macOS.
	virtualDeviceKeywords := []string{
		"BlackHole", "blackhole",
		"Loopback", "loopback",
		"Soundflower", "soundflower",
		"Multi-Output", "multi-output",
	}

	for _, d := range devices {
		name := d.Name()
		for _, kw := range virtualDeviceKeywords {
			if strings.Contains(name, kw) {
				id := d.ID
				return &id, name, nil
			}
		}
	}

	return nil, "", fmt.Errorf("no virtual audio device found — install BlackHole or similar")
}

// stopDevice stops and frees a single malgo device.
func (d *malgoDevice) stop() {
	d.device.Uninit()
	d.ctx.Uninit()
	d.ctx.Free()
	log.Printf("[audio] %s capture stopped", d.label)
}

// Start begins capturing audio from the configured source(s).
//
// onData is invoked on an internal goroutine with chunks of 16-bit PCM samples.
// The caller MUST NOT block inside onData — copy or append the data promptly.
func (c *MalgoCapturer) Start(ctx context.Context, onData func(pcmData []int16)) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.running {
		return fmt.Errorf("audio: capturer already running")
	}

	switch c.source {
	case SourceMic:
		dev, err := startCaptureDevice(malgo.Capture, nil, onData, "mic")
		if err != nil {
			return err
		}
		c.devices = []*malgoDevice{dev}

	case SourceSpeaker:
		dev, err := startSpeakerCapture(onData)
		if err != nil {
			return err
		}
		c.devices = []*malgoDevice{dev}

	case SourceBoth:
		// Start both mic and loopback; both feed into the same onData callback.
		// Thread-safety: onData is already expected to handle concurrent calls.
		micDev, err := startCaptureDevice(malgo.Capture, nil, onData, "mic")
		if err != nil {
			return err
		}
		speakerDev, err := startSpeakerCapture(onData)
		if err != nil {
			// Clean up mic if speaker fails.
			micDev.stop()
			return fmt.Errorf("audio: speaker capture failed: %w", err)
		}
		c.devices = []*malgoDevice{micDev, speakerDev}
	}

	c.running = true

	// Respect context cancellation.
	go func() {
		<-ctx.Done()
		_ = c.Stop()
	}()

	return nil
}

// Stop halts audio capture and releases all OS resources.
func (c *MalgoCapturer) Stop() error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if !c.running {
		return nil
	}

	for _, d := range c.devices {
		d.stop()
	}
	c.devices = nil
	c.running = false
	return nil
}

// ---------------------------------------------------------------------------
// Device enumeration — for diagnostics and device selection
// ---------------------------------------------------------------------------

// AudioDeviceInfo describes an available audio device.
type AudioDeviceInfo struct {
	Name      string
	ID        string
	IsDefault bool
	Type      string // "capture" or "playback"
}

// ListCaptureDevices returns information about all available capture devices.
// This is useful for diagnostics and for selecting a specific device.
func ListCaptureDevices() ([]AudioDeviceInfo, error) {
	mctx, err := initMalgoContext()
	if err != nil {
		return nil, fmt.Errorf("failed to init malgo context: %w", err)
	}
	defer func() {
		mctx.Uninit()
		mctx.Free()
	}()

	devices, err := mctx.Context.Devices(malgo.Capture)
	if err != nil {
		return nil, fmt.Errorf("failed to enumerate capture devices: %w", err)
	}

	result := make([]AudioDeviceInfo, len(devices))
	for i, d := range devices {
		result[i] = AudioDeviceInfo{
			Name:      d.Name(),
			ID:        d.ID.String(),
			IsDefault: d.IsDefault != 0,
			Type:      "capture",
		}
	}
	return result, nil
}

// ListPlaybackDevices returns information about all available playback devices.
func ListPlaybackDevices() ([]AudioDeviceInfo, error) {
	mctx, err := initMalgoContext()
	if err != nil {
		return nil, fmt.Errorf("failed to init malgo context: %w", err)
	}
	defer func() {
		mctx.Uninit()
		mctx.Free()
	}()

	devices, err := mctx.Context.Devices(malgo.Playback)
	if err != nil {
		return nil, fmt.Errorf("failed to enumerate playback devices: %w", err)
	}

	result := make([]AudioDeviceInfo, len(devices))
	for i, d := range devices {
		result[i] = AudioDeviceInfo{
			Name:      d.Name(),
			ID:        d.ID.String(),
			IsDefault: d.IsDefault != 0,
			Type:      "playback",
		}
	}
	return result, nil
}
