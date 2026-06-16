//go:build ignore

// Benchmark script: Whisper (ggml-small) vs Nemotron ASR on Turkish + English audio.
//
// Run from gleann-sound directory:
//   go run ./test/benchmark_asr.go
//
// Output: latency, RTF (real-time factor), transcribed text, approximate WER.
package main

import (
	"context"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	ort "github.com/yalue/onnxruntime_go"

	"github.com/tevfik/gleann-plugin-sound/internal/core"
	_ "github.com/tevfik/gleann-plugin-sound/internal/nemotron"
)

// ─── Test cases ────────────────────────────────────────────────────────────────

type TestCase struct {
	Name      string
	WAVPath   string
	Language  string
	Reference string // ground truth for WER calculation
}

var testCases = []TestCase{
	{
		Name:      "JFK English",
		WAVPath:   "test/testdata/jfk.wav",
		Language:  "en",
		Reference: "And so my fellow Americans ask not what your country can do for you ask what you can do for your country",
	},
	{
		Name:      "Turkish Speech",
		WAVPath:   "test/testdata/tr_test.wav",
		Language:  "tr",
		Reference: "Merhaba sesli klavye eklentisi başarıyla çalışıyor",
	},
}

// ─── Models to benchmark ───────────────────────────────────────────────────────

type ModelConfig struct {
	Name    string
	Backend string
	Path    string
	Lang    string
}

// ─── WER calculation ──────────────────────────────────────────────────────────

func normalizeText(s string) []string {
	s = strings.ToLower(s)
	// Remove punctuation
	var b strings.Builder
	for _, r := range s {
		if r == ' ' || (r >= 'a' && r <= 'z') || (r >= 'ğ' && r <= 'ü') ||
			r == 'ş' || r == 'ı' || r == 'ö' || r == 'ü' || r == 'ğ' || r == 'ç' {
			b.WriteRune(r)
		} else if r == ',' || r == '.' || r == '!' || r == '?' {
			b.WriteRune(' ')
		}
	}
	words := strings.Fields(b.String())
	return words
}

func editDistance(a, b []string) int {
	la, lb := len(a), len(b)
	dp := make([][]int, la+1)
	for i := range dp {
		dp[i] = make([]int, lb+1)
	}
	for i := 0; i <= la; i++ {
		dp[i][0] = i
	}
	for j := 0; j <= lb; j++ {
		dp[0][j] = j
	}
	for i := 1; i <= la; i++ {
		for j := 1; j <= lb; j++ {
			if a[i-1] == b[j-1] {
				dp[i][j] = dp[i-1][j-1]
			} else {
				dp[i][j] = 1 + min3(dp[i-1][j], dp[i][j-1], dp[i-1][j-1])
			}
		}
	}
	return dp[la][lb]
}

func min3(a, b, c int) int {
	if a < b {
		if a < c {
			return a
		}
		return c
	}
	if b < c {
		return b
	}
	return c
}

func wer(ref, hyp string) float64 {
	refWords := normalizeText(ref)
	hypWords := normalizeText(hyp)
	if len(refWords) == 0 {
		if len(hypWords) == 0 {
			return 0.0
		}
		return 1.0
	}
	ed := editDistance(refWords, hypWords)
	return float64(ed) / float64(len(refWords))
}

// ─── Audio loading ─────────────────────────────────────────────────────────────

func loadWAVAsPCM(path string) ([]int16, float64, error) {
	cmd := exec.Command("ffmpeg",
		"-i", path,
		"-ar", "16000",
		"-ac", "1",
		"-f", "s16le",
		"-acodec", "pcm_s16le",
		"-",
	)
	raw, err := cmd.Output()
	if err != nil {
		return nil, 0, fmt.Errorf("ffmpeg: %w", err)
	}
	nSamples := len(raw) / 2
	pcm := make([]int16, nSamples)
	for i := 0; i < nSamples; i++ {
		pcm[i] = int16(uint16(raw[i*2]) | uint16(raw[i*2+1])<<8)
	}
	durSec := float64(nSamples) / 16000.0
	return pcm, durSec, nil
}

// ─── Result struct ─────────────────────────────────────────────────────────────

type BenchResult struct {
	ModelName string
	TestCase  string
	Duration  float64 // audio duration seconds
	Elapsed   float64 // inference time seconds
	RTF       float64 // real-time factor (elapsed/duration, lower=faster)
	Text      string
	WER       float64
}

// ─── Main ──────────────────────────────────────────────────────────────────────

func main() {
	// Discover ONNX runtime for nemotron
	ortLib := os.Getenv("ORT_LIB_PATH")
	if ortLib == "" {
		// Try common locations
		candidates := []string{
			"/home/tevfik/WORKSPACE/nemotron-onnx-go/lib/libonnxruntime.so",
			"/home/tevfik/.gleann/lib/libonnxruntime.so.1.26.0",
			"/usr/lib/libonnxruntime.so",
		}
		for _, c := range candidates {
			if _, err := os.Stat(c); err == nil {
				ortLib = c
				break
			}
		}
	}
	if ortLib != "" {
		ort.SetSharedLibraryPath(ortLib)
		if err := ort.InitializeEnvironment(); err != nil && !strings.Contains(err.Error(), "already") {
			fmt.Printf("WARN: ORT init: %v\n", err)
		}
	}

	// Build model configs from available files
	home := os.Getenv("HOME")
	modelDir := filepath.Join(home, ".gleann", "models")

	models := []ModelConfig{}

	// Nemotron models
	nemotronDir := filepath.Join(modelDir, "nemotron-3.5-asr-int4-onnx")
	if _, err := os.Stat(filepath.Join(nemotronDir, "encoder.onnx")); err == nil {
		models = append(models, ModelConfig{Name: "Nemotron-3.5-0.6B (int4)", Backend: "nemotron", Path: nemotronDir})
	}
	// Also try the full-precision nemotron
	nemotronFPDir := "/home/tevfik/WORKSPACE/nemotron-onnx-go/model"
	if _, err := os.Stat(filepath.Join(nemotronFPDir, "encoder.onnx")); err == nil {
		models = append(models, ModelConfig{Name: "Nemotron-3.5-0.6B (fp32)", Backend: "nemotron", Path: nemotronFPDir})
	}

	if len(models) == 0 {
		fmt.Println("ERROR: No models found. Check paths.")
		os.Exit(1)
	}

	fmt.Println("╔══════════════════════════════════════════════════════════════════════════╗")
	fmt.Println("║              Nemotron ASR Benchmark                                      ║")
	fmt.Println("╚══════════════════════════════════════════════════════════════════════════╝")
	fmt.Printf("\nFound %d model(s) to benchmark\n\n", len(models))

	var results []BenchResult

	for _, model := range models {
		fmt.Printf("▶ Loading: %s (%s)\n", model.Name, model.Path)
		engine, err := core.NewTranscriber(model.Backend, model.Path)
		if err != nil {
			fmt.Printf("  ERROR loading model: %v\n", err)
			continue
		}

		for _, tc := range testCases {
			fmt.Printf("  ├─ Test: %s (lang=%s)\n", tc.Name, tc.Language)
			engine.SetLanguage(tc.Language)

			// Load audio
			pcm, durSec, err := loadWAVAsPCM(tc.WAVPath)
			if err != nil {
				fmt.Printf("  │  ERROR loading audio: %v\n", err)
				continue
			}

			// Warm-up run (discarded)
			_, _ = engine.TranscribeStream(context.Background(), pcm[:min2(len(pcm), 16000)])

			// Timed runs — average over N iterations
			const runs = 3
			var totalElapsed float64
			var lastText string
			for i := 0; i < runs; i++ {
				start := time.Now()
				segs, err := engine.TranscribeStream(context.Background(), pcm)
				elapsed := time.Since(start).Seconds()
				if err != nil {
					fmt.Printf("  │  ERROR transcribing (run %d): %v\n", i+1, err)
					break
				}
				lastText = strings.TrimSpace(segs)
				totalElapsed += elapsed
			}

			avgElapsed := totalElapsed / runs
			rtf := avgElapsed / durSec
			werScore := wer(tc.Reference, lastText)

			results = append(results, BenchResult{
				ModelName: model.Name,
				TestCase:  tc.Name,
				Duration:  durSec,
				Elapsed:   avgElapsed,
				RTF:       rtf,
				Text:      lastText,
				WER:       werScore,
			})

			fmt.Printf("  │  Audio: %.2fs | Latency: %.3fs avg (%d runs)\n", durSec, avgElapsed, runs)
			fmt.Printf("  │  RTF:   %.3f (%.1fx real-time)\n", rtf, 1.0/math.Max(rtf, 0.001))
			fmt.Printf("  │  WER:   %.1f%%\n", werScore*100)
			fmt.Printf("  └─ Text: %q\n\n", lastText)
		}

		engine.Close()
	}

	// ── Summary table ────────────────────────────────────────────────────────
	fmt.Println("\n╔══════════════════════════════════════════════════════════════════════════╗")
	fmt.Println("║  SUMMARY                                                                 ║")
	fmt.Println("╠══════════════════════════════════════════════════════════════════════════╣")
	fmt.Printf("║  %-28s  %-18s  %7s  %6s  %5s  ║\n", "Model", "Test", "Latency", "RTF", "WER")
	fmt.Println("╠══════════════════════════════════════════════════════════════════════════╣")
	for _, r := range results {
		modelShort := r.ModelName
		if len(modelShort) > 28 {
			modelShort = modelShort[:25] + "..."
		}
		testShort := r.TestCase
		if len(testShort) > 18 {
			testShort = testShort[:15] + "..."
		}
		fmt.Printf("║  %-28s  %-18s  %6.3fs  %5.3f  %4.1f%%  ║\n",
			modelShort, testShort, r.Elapsed, r.RTF, r.WER*100)
	}
	fmt.Println("╚══════════════════════════════════════════════════════════════════════════╝")
	fmt.Println("\nNote: RTF < 1.0 = faster than real-time. Lower WER = better accuracy.")
}

func min2(a, b int) int {
	if a < b {
		return a
	}
	return b
}
