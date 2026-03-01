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
// MalgoCapturer
// ---------------------------------------------------------------------------

// MalgoCapturer implements core.AudioCapturer using the MiniAudio library
// (via malgo) for cross-platform audio input.
//
// It captures from the default recording device, converts the raw bytes to
// []int16 PCM, and delivers them through the onData callback.
type MalgoCapturer struct {
	mu      sync.Mutex
	ctx     *malgo.AllocatedContext
	device  *malgo.Device
	running bool
}

// Compile-time interface check.
var _ core.AudioCapturer = (*MalgoCapturer)(nil)

// NewMalgoCapturer creates and returns an uninitialised capturer.
// Call Start to begin recording.
func NewMalgoCapturer() *MalgoCapturer {
	return &MalgoCapturer{}
}

// Start begins capturing audio from the default input device at 16 kHz mono.
//
// onData is invoked on an internal goroutine with chunks of 16-bit PCM samples.
// The caller MUST NOT block inside onData — copy or append the data promptly.
func (c *MalgoCapturer) Start(ctx context.Context, onData func(pcmData []int16)) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.running {
		return fmt.Errorf("audio: capturer already running")
	}

	// Initialise the MiniAudio context (backend auto-detection).
	// Order matters: first match wins.
	// PulseAudio covers PipeWire (via pipewire-pulse) and classic PulseAudio.
	// ALSA is the fallback for Linux (works when running as root or without
	// a user session, e.g. systemd services).
	backends := []malgo.Backend{
		malgo.BackendPulseaudio, // Linux (PipeWire compat via pipewire-pulse)
		malgo.BackendAlsa,       // Linux fallback (direct ALSA)
		malgo.BackendWasapi,     // Windows
		malgo.BackendCoreaudio,  // macOS
	}

	mctx, err := malgo.InitContext(backends, malgo.ContextConfig{}, nil)
	if err != nil {
		return fmt.Errorf("audio: failed to init malgo context: %w", err)
	}
	c.ctx = mctx

	// Configure capture device.
	deviceConfig := malgo.DefaultDeviceConfig(malgo.Capture)
	deviceConfig.Capture.Format = malgo.FormatS16
	deviceConfig.Capture.Channels = WhisperChannels
	deviceConfig.SampleRate = WhisperSampleRate
	deviceConfig.PeriodSizeInFrames = captureFrameSize
	deviceConfig.Alsa.NoMMap = 1

	// The data callback converts the raw byte buffer to []int16 and forwards it.
	onRecvFrames := func(outputSamples, inputSamples []byte, framecount uint32) {
		// inputSamples contains signed 16-bit little-endian PCM data.
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

	// Wrap callbacks.
	callbacks := malgo.DeviceCallbacks{
		Data: onRecvFrames,
	}

	dev, err := malgo.InitDevice(c.ctx.Context, deviceConfig, callbacks)
	if err != nil {
		c.ctx.Uninit()
		c.ctx.Free()
		c.ctx = nil
		return fmt.Errorf("audio: failed to init capture device: %w", err)
	}
	c.device = dev

	if err := c.device.Start(); err != nil {
		c.device.Uninit()
		c.ctx.Uninit()
		c.ctx.Free()
		c.ctx = nil
		c.device = nil
		return fmt.Errorf("audio: failed to start capture device: %w", err)
	}

	c.running = true
	log.Println("[audio] capture started — 16 kHz / 16-bit / mono")

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

	c.device.Uninit()
	c.ctx.Uninit()
	c.ctx.Free()
	c.device = nil
	c.ctx = nil
	c.running = false

	log.Println("[audio] capture stopped")
	return nil
}
