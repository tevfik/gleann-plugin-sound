package main

import (
	"context"
	"fmt"
	"log"
	"math"
	"os"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/spf13/cobra"
	"github.com/tevfik/gleann-sound/internal/audio"
	"github.com/tevfik/gleann-sound/internal/config"
	"github.com/tevfik/gleann-sound/internal/core"
	"github.com/tevfik/gleann-sound/internal/hotkey"
	"github.com/tevfik/gleann-sound/internal/keyboard"
)

// newTestCmd creates the "test" subcommand for diagnosing hardware & config.
func newTestCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "test",
		Short: "Test microphone, hotkey, transcription & keyboard injection",
		Long: `Runs a quick diagnostic to verify that:

  1. Microphone capture is working (records 3 seconds, shows audio level)
  2. Hotkey detection works (waits for a key press/release event)
  3. Whisper transcription works (transcribes a short recording)
  4. Keyboard injection works (types a test string into stdout)

Uses the config from ~/.gleann/sound.json or CLI flags.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			modelPath, _ := cmd.Flags().GetString("model")
			hotkeyStr, _ := cmd.Flags().GetString("key")
			lang, _ := cmd.Flags().GetString("language")

			// Load config if flags not specified.
			cfg := config.Load()
			if modelPath == "" && cfg != nil && cfg.DefaultModel != "" {
				modelPath = cfg.DefaultModel
			}
			if hotkeyStr == "" && cfg != nil && cfg.Hotkey != "" {
				hotkeyStr = cfg.Hotkey
			}
			if hotkeyStr == "" {
				hotkeyStr = "f9"
			}
			if lang == "" && cfg != nil && cfg.Language != "" {
				lang = cfg.Language
			}

			printHeader("gleann-sound Diagnostic Test")
			fmt.Println()

			// ── Step 1: Microphone ─────────────────────────────
			printStep(1, "Microphone Capture")
			micOK, pcm := testMicrophone()
			fmt.Println()

			// ── Step 2: Hotkey ─────────────────────────────────
			printStep(2, "Hotkey Detection")
			hotkeyOK := testHotkey(hotkeyStr)
			fmt.Println()

			// ── Step 3: Whisper ────────────────────────────────
			printStep(3, "Whisper Transcription")
			whisperOK := testWhisper(modelPath, lang, pcm)
			fmt.Println()

			// ── Step 4: Keyboard ───────────────────────────────
			printStep(4, "Keyboard Injection")
			kbOK := testKeyboard()
			fmt.Println()

			// ── Summary ────────────────────────────────────────
			printHeader("Results")
			printResult("Microphone", micOK)
			printResult("Hotkey", hotkeyOK)
			printResult("Whisper", whisperOK)
			printResult("Keyboard", kbOK)
			fmt.Println()

			if micOK && hotkeyOK && whisperOK && kbOK {
				fmt.Println("  ✅ All tests passed — dictation should work!")
			} else {
				fmt.Println("  ⚠  Some tests failed — check the output above for details.")
			}
			fmt.Println()

			return nil
		},
	}

	cmd.Flags().String("key", "", "Hotkey to test (default: from config or f9)")
	cmd.Flags().String("language", "", "Language for transcription test")

	return cmd
}

// ── Step implementations ───────────────────────────────────────

func testMicrophone() (bool, []int16) {
	fmt.Println("  Recording 3 seconds from default microphone...")

	capturer := audio.NewMalgoCapturer()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	var mu sync.Mutex
	var allPCM []int16
	var maxAmp int16

	err := capturer.Start(ctx, func(pcm []int16) {
		mu.Lock()
		allPCM = append(allPCM, pcm...)
		for _, s := range pcm {
			if s < 0 {
				s = -s
			}
			if s > maxAmp {
				maxAmp = s
			}
		}
		mu.Unlock()
	})
	if err != nil {
		fmt.Printf("  ✗ Failed to start capture: %v\n", err)
		return false, nil
	}

	// Show a simple progress bar.
	for i := 0; i < 30; i++ {
		time.Sleep(100 * time.Millisecond)
		if i%10 == 0 {
			fmt.Printf("  ▪ %ds...\n", i/10+1)
		}
	}

	cancel()
	_ = capturer.Stop()

	mu.Lock()
	samples := len(allPCM)
	peak := maxAmp
	pcmCopy := make([]int16, len(allPCM))
	copy(pcmCopy, allPCM)
	mu.Unlock()

	if samples == 0 {
		fmt.Println("  ✗ No audio captured (0 samples)")
		return false, nil
	}

	duration := float64(samples) / float64(audio.WhisperSampleRate)
	dbFS := 20 * math.Log10(float64(peak)/32768.0)
	level := levelBar(peak)

	fmt.Printf("  ✓ Captured %.2fs (%d samples)\n", duration, samples)
	fmt.Printf("  ✓ Peak level: %d (%.1f dBFS) %s\n", peak, dbFS, level)

	if peak < 100 {
		fmt.Println("  ⚠  Audio level very low — is your microphone muted?")
	}

	return true, pcmCopy
}

func testHotkey(hotkeyStr string) bool {
	fmt.Printf("  Testing hotkey: %s\n", hotkeyStr)
	fmt.Printf("  Press and release <%s> within 10 seconds...\n", hotkeyStr)

	mods, key, err := parseHotkey(hotkeyStr)
	if err != nil {
		fmt.Printf("  ✗ Invalid hotkey %q: %v\n", hotkeyStr, err)
		return false
	}

	runtime.LockOSThread()
	defer runtime.UnlockOSThread()

	hk := hotkey.New(mods, key)
	if err := hk.Register(); err != nil {
		fmt.Printf("  ✗ Failed to register: %v\n", err)
		return false
	}
	defer hk.Unregister()

	fmt.Println("  ✓ Hotkey registered, waiting for keypress...")

	// Wait for keydown.
	timeout := time.After(10 * time.Second)
	select {
	case <-hk.Keydown():
		fmt.Println("  ✓ Keydown detected!")
	case <-timeout:
		fmt.Println("  ✗ Timeout — no keypress detected in 10 seconds")
		return false
	}

	// Wait for keyup.
	timeout2 := time.After(10 * time.Second)
	select {
	case <-hk.Keyup():
		fmt.Println("  ✓ Keyup detected!")
	case <-timeout2:
		fmt.Println("  ✗ Timeout — no key release detected")
		return false
	}

	fmt.Println("  ✓ Hotkey works correctly!")
	return true
}

func testWhisper(modelPath, lang string, pcm []int16) bool {
	if modelPath == "" {
		fmt.Println("  ✗ No model path specified (use --model or run setup)")
		return false
	}

	if _, err := os.Stat(modelPath); os.IsNotExist(err) {
		fmt.Printf("  ✗ Model file not found: %s\n", modelPath)
		return false
	}

	fmt.Printf("  Loading model: %s\n", modelPath)
	backend := "whisper" // test always uses whisper for now
	engine, err := core.NewTranscriber(backend, modelPath)
	if err != nil {
		fmt.Printf("  ✗ Failed to load: %v\n", err)
		return false
	}
	defer engine.Close()
	fmt.Println("  ✓ Model loaded")

	if lang != "" {
		engine.SetLanguage(lang)
		fmt.Printf("  Language: %s\n", lang)
	}

	// If we have PCM from the microphone test, transcribe it.
	if len(pcm) > 0 {
		fmt.Println("  Transcribing microphone recording...")
		start := time.Now()
		text, err := engine.TranscribeStream(context.Background(), pcm)
		elapsed := time.Since(start)
		if err != nil {
			fmt.Printf("  ✗ Transcription error: %v\n", err)
			return false
		}

		text = strings.TrimSpace(text)
		if text == "" || text == "[BLANK_AUDIO]" || text == "(inaudible)" {
			fmt.Printf("  ✓ Whisper returned empty/blank (silence detected) in %v\n", elapsed)
			fmt.Println("  ⚠  Try speaking louder during the microphone test")
		} else {
			fmt.Printf("  ✓ Transcribed in %v: %q\n", elapsed, text)
		}
	} else {
		fmt.Println("  ⚠  No audio to transcribe (microphone test failed)")
		fmt.Println("  ✓ Model loads correctly — whisper engine OK")
	}

	return true
}

func testKeyboard() bool {
	fmt.Println("  Testing keyboard injection...")

	injector := keyboard.NewRobotGoInjector()

	// Type a simple test into stdout.
	testStr := "gleann-sound test ✓"
	fmt.Printf("  Injecting: %q\n", testStr)
	fmt.Print("  Output: ")

	if err := injector.TypeText(testStr); err != nil {
		fmt.Printf("\n  ✗ Injection error: %v\n", err)

		if runtime.GOOS == "linux" {
			fmt.Println("  Hint: keyboard injection needs X11 (DISPLAY) or Wayland (WAYLAND_DISPLAY)")
			if os.Getenv("DISPLAY") == "" && os.Getenv("WAYLAND_DISPLAY") == "" {
				fmt.Println("  ✗ Neither DISPLAY nor WAYLAND_DISPLAY is set")
			}
		}
		return false
	}

	fmt.Println()
	fmt.Println("  ✓ Keyboard injection works!")
	return true
}

// ── Helpers ────────────────────────────────────────────────────

func printHeader(title string) {
	line := strings.Repeat("─", 50)
	fmt.Printf("  %s\n", line)
	fmt.Printf("  %s\n", title)
	fmt.Printf("  %s\n", line)
}

func printStep(n int, name string) {
	fmt.Printf("  ── Step %d: %s ──\n", n, name)
}

func printResult(name string, ok bool) {
	if ok {
		fmt.Printf("  ✓ %-20s PASS\n", name)
	} else {
		fmt.Printf("  ✗ %-20s FAIL\n", name)
	}
}

func levelBar(peak int16) string {
	// Map peak (0-32767) to a bar of 0-20 blocks.
	n := int(float64(peak) / 32768.0 * 20)
	if n < 0 {
		n = 0
	}
	if n > 20 {
		n = 20
	}
	bar := strings.Repeat("█", n) + strings.Repeat("░", 20-n)
	return "[" + bar + "]"
}

// suppress unused linter for log import
var _ = log.Println
