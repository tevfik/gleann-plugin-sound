package main

import (
	"context"
	"fmt"
	"log"
	"math"
	"os"
	"os/signal"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/spf13/cobra"
	"github.com/tevfik/gleann-plugin-sound/internal/audio"
	"github.com/tevfik/gleann-plugin-sound/internal/config"
	"github.com/tevfik/gleann-plugin-sound/internal/core"
	"github.com/tevfik/gleann-plugin-sound/internal/hotkey"
	"github.com/tevfik/gleann-plugin-sound/internal/keyboard"
	"github.com/tevfik/gleann-plugin-sound/internal/pipeline"
)

// newDictateCmd creates the "dictate" subcommand (Mode 4: Voice Dictation).
//
// Usage:
//
//	gleann-plugin-sound dictate --key "ctrl+alt+space" --model models/ggml-base.en.bin
//
// Registers a global hotkey.  While the key is held, audio is captured from
// the default mic.  On release, the audio is transcribed via Whisper and the
// resulting text is injected as simulated keystrokes into the active window.
func newDictateCmd() *cobra.Command {
	var hotkeyStr string

	cmd := &cobra.Command{
		Use:   "dictate",
		Short: "Push-to-talk voice dictation with keystroke injection",
		Long: `Mode 4 — Voice Dictation.

Registers a global system hotkey (e.g. Ctrl+Alt+Space).  Hold the key and
speak; release the key to transcribe and inject the text as keystrokes into
the currently active window.

This mode requires:
  - A working microphone accessible via PulseAudio/PipeWire (Linux) or WASAPI (Windows)
  - Whisper model downloaded locally
  - X11/Wayland permissions for keystroke injection (Linux) or appropriate
    accessibility permissions (macOS)

Press Ctrl+C to exit.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			modelPath, _ := cmd.Flags().GetString("model")
			lang, _ := cmd.Flags().GetString("language")
			verbose, _ := cmd.Flags().GetBool("verbose")

			// ── Config fallback: read from ~/.gleann/sound.json ──
			if cfg := config.Load(); cfg != nil {
				if modelPath == "" && cfg.DefaultModel != "" {
					modelPath = cfg.DefaultModel
				}
				if hotkeyStr == "ctrl+alt+space" && cfg.Hotkey != "" {
					// Only override if the flag is still the default.
					hotkeyStr = cfg.Hotkey
				}
				if lang == "" && cfg.Language != "" {
					lang = cfg.Language
				}
				log.Printf("[dictate] config loaded from %s", config.ConfigPath())
			}

			if modelPath == "" {
				return fmt.Errorf("no model specified: use --model flag or run TUI setup")
			}

			log.Println("[dictate] initialising...")

			// ── Parse hotkey string ────────────────────────────────
			mods, key, err := parseHotkey(hotkeyStr)
			if err != nil {
				return fmt.Errorf("invalid hotkey %q: %w", hotkeyStr, err)
			}
			log.Printf("[dictate] hotkey parsed: %s", hotkeyStr)

			// ── Initialise transcription engine ──────────────────
			backend, _ := cmd.Flags().GetString("backend")
			log.Printf("[dictate] loading model: %s (backend: %s)", modelPath, backend)
			engine, err := core.NewTranscriber(backend, modelPath)
			if err != nil {
				return fmt.Errorf("failed to load model: %w", err)
			}
			defer engine.Close()

			// Set language for multilingual models.
			if lang != "" {
				engine.SetLanguage(lang)
				log.Printf("[dictate] language set to: %s", lang)
			} else {
				log.Println("[dictate] language: auto-detect")
			}

			// ── Initialise audio capturer ──────────────────────────
			capturer := audio.NewMalgoCapturer()
			log.Println("[dictate] audio capturer initialised")

			// ── Initialise keyboard injector ───────────────────────
			injector := keyboard.NewRobotGoInjector()
			log.Println("[dictate] keyboard injector initialised")

			// ── Signal handling ────────────────────────────────────
			ctx, cancel := context.WithCancel(cmd.Context())
			defer cancel()

			sigCh := make(chan os.Signal, 1)
			signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
			go func() {
				sig := <-sigCh
				log.Printf("[dictate] received signal %v, shutting down…", sig)
				cancel()
				// Force exit after 3 seconds if graceful shutdown hangs.
				time.AfterFunc(3*time.Second, func() {
					log.Println("[dictate] force exit (shutdown timeout)")
					os.Exit(1)
				})
			}()

			// ── Register global hotkey ─────────────────────────────
			// The hotkey library requires all registration and event
			// handling to happen on the main OS thread (especially on
			// macOS/Windows with message loops).
			runtime.LockOSThread()
			defer runtime.UnlockOSThread()

			log.Printf("[dictate] registering hotkey: %s", hotkeyStr)
			hk := hotkey.New(mods, key)
			hk.Verbose = verbose
			if err := hk.Register(); err != nil {
				return fmt.Errorf("failed to register hotkey %q: %w", hotkeyStr, err)
			}
			defer func() { _ = hk.Unregister() }()

			log.Printf("[dictate] ✓ ready — hold <%s> to speak, release to transcribe", hotkeyStr)
			log.Println("[dictate] press Ctrl+C to exit")

			// ── Push-to-talk event loop ────────────────────────────
			return dictateLoop(ctx, hk, capturer, engine, injector, backend)
		},
	}

	cmd.Flags().StringVar(&hotkeyStr, "key", "ctrl+alt+space",
		"Global hotkey combination (e.g. ctrl+alt+space, ctrl+shift+d)")
	cmd.Flags().String("model", "",
		"Path to Whisper model file. Falls back to config default_model")
	cmd.Flags().String("language", "",
		"Language code for transcription (e.g. tr, en, de). Empty = auto-detect")

	return cmd
}

// dictateLoop implements the core push-to-talk cycle with streaming transcription.
//
// Pipeline (streaming mode):
//  1. Wait for hotkey press   → start recording + streaming pipeline
//  2. While held, pipeline transcribes 3s windows every 2s → inject text incrementally
//  3. Wait for hotkey release → flush remaining audio → inject final text
//  4. Immediately ready for next press
//
// Falls back to batch mode if the engine doesn't support StreamingTranscriber.
func dictateLoop(
	ctx context.Context,
	hk *hotkey.Hotkey,
	capturer *audio.MalgoCapturer,
	engine core.Transcriber,
	injector *keyboard.RobotGoInjector,
	backend string,
) error {
	// Check if engine supports streaming.
	streamEngine, isStreaming := engine.(core.StreamingTranscriber)

	if isStreaming {
		log.Println("[dictate] using streaming pipeline (incremental injection)")
		return dictateLoopStreaming(ctx, hk, capturer, streamEngine, injector, backend)
	}

	log.Println("[dictate] using batch mode (engine doesn't support streaming)")
	return dictateLoopBatch(ctx, hk, capturer, engine, injector)
}

// dictateLoopStreaming implements push-to-talk with real-time streaming.
// Text appears incrementally while the user is still holding the hotkey.
func dictateLoopStreaming(
	ctx context.Context,
	hk *hotkey.Hotkey,
	capturer *audio.MalgoCapturer,
	engine core.StreamingTranscriber,
	injector *keyboard.RobotGoInjector,
	backend string,
) error {
	vad := audio.DefaultVAD()

	for {
		// ── Wait for Keydown ───────────────────────────────────
		log.Println("[dictate] waiting for hotkey press...")
		if !waitForEvent(ctx, hk.Keydown()) {
			break
		}
		log.Println("[dictate] hotkey pressed — streaming transcription...")

		// Reset stream context for fresh session.
		engine.ResetStream()
		vad.Reset()

		// ── Start capture into channel ─────────────────────────
		audioCh := make(chan []int16, 128)
		captureCtx, captureCancel := context.WithCancel(ctx)

		err := capturer.Start(captureCtx, func(pcmData []int16) {
			chunk := make([]int16, len(pcmData))
			copy(chunk, pcmData)
			select {
			case audioCh <- chunk:
			default:
				// Drop if backed up — should not happen.
			}
		})
		if err != nil {
			captureCancel()
			log.Printf("[dictate] capture error: %v", err)
			continue
		}

		// ── Run streaming pipeline in background ───────────────
		pipeCfg := pipeline.DefaultConfig()
		if backend == "onnx" || backend == "nemotron" {
			pipeCfg = pipeline.ONNXConfig()
		}
		pipe := pipeline.NewStreamingPipeline(engine, vad, pipeCfg)

		// Pipeline uses parent ctx — NOT captureCtx — so it can still
		// flush remaining audio after capture is stopped.
		pipeCtx, pipeCancel := context.WithCancel(ctx)
		var pipeWg sync.WaitGroup
		pipeWg.Add(1)
		go func() {
			defer pipeWg.Done()
			_ = pipe.Run(pipeCtx, audioCh, func(result core.StreamResult) {
				text := strings.TrimSpace(result.Text)
				if text == "" {
					return
				}
				log.Printf("[dictate] streaming: %q", text)
				// Add trailing space for natural word separation.
				if err := injector.TypeText(text + " "); err != nil {
					log.Printf("[dictate] injection error: %v", err)
				}
			})
		}()

		// ── Wait for Keyup ─────────────────────────────────────
		if !waitForEvent(ctx, hk.Keyup()) {
			// Ctrl+C / shutdown — cancel everything and exit.
			captureCancel()
			_ = capturer.Stop()
			close(audioCh)
			pipeCancel()
			pipeWg.Wait()
			break
		}
		log.Println("[dictate] hotkey released — flushing...")

		// ── Stop capture, close channel, let pipeline flush ────
		captureCancel()
		_ = capturer.Stop()
		close(audioCh)
		// Pipeline sees closed channel → flushes remaining audio.
		// Wait for flush to complete BEFORE cancelling pipeline context.
		pipeWg.Wait()
		pipeCancel()

		// Force GC to free audio buffers.
		runtime.GC()
	}

	return nil
}

// dictateLoopBatch is the legacy batch transcription mode for engines that
// don't support StreamingTranscriber. Kept as fallback.
func dictateLoopBatch(
	ctx context.Context,
	hk *hotkey.Hotkey,
	capturer *audio.MalgoCapturer,
	engine core.Transcriber,
	injector *keyboard.RobotGoInjector,
) error {
	type txJob struct {
		pcm []int16
		seq int
	}
	jobCh := make(chan txJob, 8)

	var pipeWg sync.WaitGroup
	pipeWg.Add(1)
	go func() {
		defer pipeWg.Done()
		for j := range jobCh {
			dur := float64(len(j.pcm)) / float64(audio.WhisperSampleRate)
			log.Printf("[dictate] transcribing %.2fs chunk #%d …", dur, j.seq)

			start := time.Now()
			text, err := engine.TranscribeStream(ctx, j.pcm)
			j.pcm = nil
			elapsed := time.Since(start)
			if err != nil {
				log.Printf("[dictate] transcription error: %v", err)
				continue
			}
			text = strings.TrimSpace(text)
			if text == "" || text == "[BLANK_AUDIO]" {
				log.Printf("[dictate] chunk #%d: silence (%.1fs) — skipping", j.seq, dur)
				continue
			}
			log.Printf("[dictate] chunk #%d transcribed in %v: %q", j.seq, elapsed, text)

			if err := injector.TypeText(text); err != nil {
				log.Printf("[dictate] injection error: %v", err)
			}
			runtime.GC()
		}
	}()

	const chunkSamples = 30 * audio.WhisperSampleRate
	var seq atomic.Int32

	for {
		log.Println("[dictate] waiting for hotkey press...")
		if !waitForEvent(ctx, hk.Keydown()) {
			break
		}
		log.Println("[dictate] hotkey pressed — recording...")

		var (
			bufMu sync.Mutex
			buf   []int16
		)
		captureCtx, captureCancel := context.WithCancel(ctx)

		err := capturer.Start(captureCtx, func(pcmData []int16) {
			bufMu.Lock()
			buf = append(buf, pcmData...)
			if len(buf) >= chunkSamples {
				chunk := make([]int16, len(buf))
				copy(chunk, buf)
				buf = buf[:0]
				bufMu.Unlock()
				s := seq.Add(1)
				select {
				case jobCh <- txJob{pcm: chunk, seq: int(s)}:
				default:
					log.Println("[dictate] pipeline busy — dropping chunk")
				}
				return
			}
			bufMu.Unlock()
		})
		if err != nil {
			captureCancel()
			log.Printf("[dictate] capture error: %v", err)
			continue
		}

		if !waitForEvent(ctx, hk.Keyup()) {
			captureCancel()
			_ = capturer.Stop()
			break
		}
		log.Println("[dictate] hotkey released — stopped recording")

		captureCancel()
		_ = capturer.Stop()

		bufMu.Lock()
		remaining := make([]int16, len(buf))
		copy(remaining, buf)
		buf = nil
		bufMu.Unlock()

		if len(remaining) > 0 {
			durSec := float64(len(remaining)) / float64(audio.WhisperSampleRate)
			if durSec < 0.3 {
				log.Println("[dictate] final chunk too short (<0.3s) — skipping")
			} else if !hasSpeech(remaining) {
				log.Printf("[dictate] final chunk %.2fs is silence — skipping", durSec)
			} else {
				s := seq.Add(1)
				select {
				case jobCh <- txJob{pcm: remaining, seq: int(s)}:
				default:
					log.Println("[dictate] pipeline busy — dropping final chunk")
				}
			}
		}
	}

	close(jobCh)
	pipeWg.Wait()
	return nil
}

// waitForEvent waits for either the context to cancel or a signal on ch.
// Returns true if ch fired, false if context cancelled.
func waitForEvent(ctx context.Context, ch <-chan struct{}) bool {
	for {
		select {
		case <-ctx.Done():
			return false
		case <-ch:
			return true
		case <-time.After(200 * time.Millisecond):
			if ctx.Err() != nil {
				return false
			}
		}
	}
}

// ---------------------------------------------------------------------------
// Hotkey string parser
// ---------------------------------------------------------------------------

// letterKeys maps single lowercase letters to their hotkey.Key constants.
// On some platforms (macOS) the key codes are NOT ASCII, so we enumerate them.
var letterKeys = map[rune]hotkey.Key{
	'a': hotkey.KeyA, 'b': hotkey.KeyB, 'c': hotkey.KeyC, 'd': hotkey.KeyD,
	'e': hotkey.KeyE, 'f': hotkey.KeyF, 'g': hotkey.KeyG, 'h': hotkey.KeyH,
	'i': hotkey.KeyI, 'j': hotkey.KeyJ, 'k': hotkey.KeyK, 'l': hotkey.KeyL,
	'm': hotkey.KeyM, 'n': hotkey.KeyN, 'o': hotkey.KeyO, 'p': hotkey.KeyP,
	'q': hotkey.KeyQ, 'r': hotkey.KeyR, 's': hotkey.KeyS, 't': hotkey.KeyT,
	'u': hotkey.KeyU, 'v': hotkey.KeyV, 'w': hotkey.KeyW, 'x': hotkey.KeyX,
	'y': hotkey.KeyY, 'z': hotkey.KeyZ,
}

// digitKeys maps digit characters to their hotkey.Key constants.
var digitKeys = map[rune]hotkey.Key{
	'0': hotkey.Key0, '1': hotkey.Key1, '2': hotkey.Key2, '3': hotkey.Key3,
	'4': hotkey.Key4, '5': hotkey.Key5, '6': hotkey.Key6, '7': hotkey.Key7,
	'8': hotkey.Key8, '9': hotkey.Key9,
}

// parseHotkey converts a human-readable hotkey string like "ctrl+alt+space"
// into the modifier and key constants expected by golang.design/x/hotkey.
//
// Supported modifiers: ctrl, alt, shift, super/win/cmd
// Supported keys: a-z, 0-9, space, return/enter, escape, f1-f12
func parseHotkey(s string) ([]hotkey.Modifier, hotkey.Key, error) {
	parts := strings.Split(strings.ToLower(strings.TrimSpace(s)), "+")
	if len(parts) == 0 {
		return nil, 0, fmt.Errorf("empty hotkey string")
	}

	var mods []hotkey.Modifier
	var key hotkey.Key
	keySet := false

	for _, p := range parts {
		p = strings.TrimSpace(p)
		switch p {
		// ── Modifiers ──────────────────────────────────────────
		// On Linux:  ModCtrl, ModShift, Mod1 (Alt), Mod4 (Super)
		// On macOS:  ModCtrl, ModShift, ModOption (Alt), ModCmd (Super)
		// On Win:    ModCtrl, ModShift, ModAlt, ModWin
		case "ctrl", "control":
			mods = append(mods, hotkey.ModCtrl)
		case "alt", "option":
			mods = append(mods, hotkey.ModAlt)
		case "shift":
			mods = append(mods, hotkey.ModShift)
		case "super", "win", "cmd", "meta":
			mods = append(mods, hotkey.ModSuper)

		// ── Special keys ───────────────────────────────────────
		case "space":
			key = hotkey.KeySpace
			keySet = true
		case "return", "enter":
			key = hotkey.KeyReturn
			keySet = true
		case "escape", "esc":
			key = hotkey.KeyEscape
			keySet = true
		case "tab":
			key = hotkey.KeyTab
			keySet = true

		// ── Function keys ──────────────────────────────────────
		case "f1":
			key = hotkey.KeyF1
			keySet = true
		case "f2":
			key = hotkey.KeyF2
			keySet = true
		case "f3":
			key = hotkey.KeyF3
			keySet = true
		case "f4":
			key = hotkey.KeyF4
			keySet = true
		case "f5":
			key = hotkey.KeyF5
			keySet = true
		case "f6":
			key = hotkey.KeyF6
			keySet = true
		case "f7":
			key = hotkey.KeyF7
			keySet = true
		case "f8":
			key = hotkey.KeyF8
			keySet = true
		case "f9":
			key = hotkey.KeyF9
			keySet = true
		case "f10":
			key = hotkey.KeyF10
			keySet = true
		case "f11":
			key = hotkey.KeyF11
			keySet = true
		case "f12":
			key = hotkey.KeyF12
			keySet = true

		default:
			// Single letter a-z or digit 0-9.
			if len(p) == 1 {
				r := rune(p[0])
				if k, ok := letterKeys[r]; ok {
					key = k
					keySet = true
				} else if k, ok := digitKeys[r]; ok {
					key = k
					keySet = true
				} else {
					return nil, 0, fmt.Errorf("unsupported key: %q", p)
				}
			} else {
				return nil, 0, fmt.Errorf("unsupported key component: %q", p)
			}
		}
	}

	if !keySet {
		return nil, 0, fmt.Errorf("no key specified in hotkey string %q", s)
	}

	return mods, key, nil
}

// hasSpeech performs a quick energy-based check on a PCM buffer to determine
// if it likely contains speech. This avoids sending pure silence/noise to
// whisper, which is both slow and produces hallucinations.
func hasSpeech(pcm []int16) bool {
	if len(pcm) == 0 {
		return false
	}

	// Compute RMS energy over the entire buffer.
	var sumSq float64
	for _, s := range pcm {
		v := float64(s)
		sumSq += v * v
	}
	rms := math.Sqrt(sumSq / float64(len(pcm)))

	// Also check how many frames (20ms windows) have energy above a floor.
	const frameSamples = audio.WhisperSampleRate / 50 // 20ms = 320 samples
	speechFrames := 0
	totalFrames := 0
	for i := 0; i+frameSamples <= len(pcm); i += frameSamples {
		var fSumSq float64
		for j := i; j < i+frameSamples; j++ {
			v := float64(pcm[j])
			fSumSq += v * v
		}
		fRMS := math.Sqrt(fSumSq / float64(frameSamples))
		totalFrames++
		if fRMS > 200.0 { // absolute floor for 16-bit PCM
			speechFrames++
		}
	}

	// Need overall RMS > 150 AND at least 10% of frames contain energy.
	speechRatio := float64(speechFrames) / float64(max(totalFrames, 1))
	return rms > 150.0 && speechRatio > 0.10
}
