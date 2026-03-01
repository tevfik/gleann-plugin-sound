package main

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"
	"github.com/tevfik/gleann-sound/internal/config"
	"github.com/tevfik/gleann-sound/internal/core"
)

// newTranscribeCmd creates the "transcribe" subcommand (Mode 1: On-Demand).
//
// Usage:
//
//	gleann-sound transcribe --file recording.mp3 --model models/ggml-base.en.bin
//
// Reads an audio file, converts it to 16 kHz PCM (via ffmpeg), runs Whisper,
// and outputs timestamped JSONL to stdout.
func newTranscribeCmd() *cobra.Command {
	var filePath string
	var outputFile string

	cmd := &cobra.Command{
		Use:   "transcribe",
		Short: "Transcribe an audio/video file to timestamped JSONL",
		Long: `Mode 1 — On-Demand File Transcription.

Reads the given media file, decodes it to 16 kHz mono PCM via ffmpeg,
runs Whisper inference, and writes one JSON object per segment to stdout.
Optionally writes output to a file with --output (defaults to <input>.jsonl).

Requires ffmpeg to be installed and available on $PATH.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if filePath == "" {
				return fmt.Errorf("--file is required")
			}

			modelPath, _ := cmd.Flags().GetString("model")
			lang, _ := cmd.Flags().GetString("language")

			log.Println("[transcribe] initialising...")

			// ── Initialise transcription engine ──────────────────
			backend, _ := cmd.Flags().GetString("backend")
			log.Printf("[transcribe] loading model: %s (backend: %s)", modelPath, backend)
			engine, err := core.NewTranscriber(backend, modelPath)
			if err != nil {
				return fmt.Errorf("failed to load model: %w", err)
			}
			defer engine.Close()

			if lang != "" {
				engine.SetLanguage(lang)
			}

			// ── Run transcription ──────────────────────────────────
			log.Printf("[transcribe] processing file: %s", filePath)
			segments, err := engine.TranscribeFile(cmd.Context(), filePath)
			if err != nil {
				return fmt.Errorf("transcription failed: %w", err)
			}

			// ── Output as JSONL ────────────────────────────────────
			// Determine output file path.
			outPath := outputFile
			if outPath != "" {
				outPath = config.ExpandPath(outPath)
			}
			if outPath == "" {
				// Default: same name as input file with .jsonl extension.
				ext := filepath.Ext(filePath)
				outPath = strings.TrimSuffix(filePath, ext) + ".jsonl"
			} else if info, statErr := os.Stat(outPath); (statErr == nil && info.IsDir()) || strings.HasSuffix(outPath, string(os.PathSeparator)) || filepath.Ext(outPath) == "" {
				// outPath is a directory — put <input>.jsonl inside it.
				if mkErr := os.MkdirAll(outPath, 0o755); mkErr != nil {
					return fmt.Errorf("failed to create output directory: %w", mkErr)
				}
				base := filepath.Base(filePath)
				ext := filepath.Ext(base)
				outPath = filepath.Join(outPath, strings.TrimSuffix(base, ext)+".jsonl")
			} else {
				// Ensure parent directory exists.
				if mkErr := os.MkdirAll(filepath.Dir(outPath), 0o755); mkErr != nil {
					return fmt.Errorf("failed to create output directory: %w", mkErr)
				}
			}

			// Write to file.
			f, err := os.Create(outPath)
			if err != nil {
				return fmt.Errorf("failed to create output file: %w", err)
			}
			defer f.Close()
			fileEnc := json.NewEncoder(f)

			// Also write to stdout.
			stdoutEnc := json.NewEncoder(os.Stdout)
			for _, seg := range segments {
				js := newJSONSegment(seg)
				if err := stdoutEnc.Encode(js); err != nil {
					return fmt.Errorf("failed to write JSON: %w", err)
				}
				if err := fileEnc.Encode(js); err != nil {
					return fmt.Errorf("failed to write to file: %w", err)
				}
			}

			log.Printf("[transcribe] done — %d segments, output: %s", len(segments), outPath)
			return nil
		},
	}

	cmd.Flags().StringVarP(&filePath, "file", "f", "", "Path to the audio/video file to transcribe")
	_ = cmd.MarkFlagRequired("file")
	cmd.Flags().StringVarP(&outputFile, "output", "o", "",
		"Output JSONL file path (default: <input>.jsonl)")
	cmd.Flags().String("language", "",
		"Language code for transcription (e.g. tr, en, de). Empty = auto-detect")

	return cmd
}

// jsonSegment is the JSONL output format for a single transcription segment.
type jsonSegment struct {
	StartMs int64  `json:"start_ms"`
	EndMs   int64  `json:"end_ms"`
	Text    string `json:"text"`
}

// newJSONSegment converts a core.Segment to the JSON output type.
func newJSONSegment(s core.Segment) jsonSegment {
	return jsonSegment{
		StartMs: s.Start.Milliseconds(),
		EndMs:   s.End.Milliseconds(),
		Text:    s.Text,
	}
}
