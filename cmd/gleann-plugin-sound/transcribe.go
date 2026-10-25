package main

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/tevfik/gleann-plugin-sound/internal/audio"
	"github.com/tevfik/gleann-plugin-sound/internal/config"
	"github.com/tevfik/gleann-plugin-sound/internal/core"
	"github.com/tevfik/gleann-plugin-sound/internal/pipeline"
	"github.com/tevfik/gleann-plugin-sound/internal/tcasr"
)

// newTranscribeCmd creates the "transcribe" subcommand (Mode 1: On-Demand).
//
// Usage:
//
//	gleann-plugin-sound transcribe --file recording.mp3 --model models/ggml-base.en.bin
//
// Reads an audio file, converts it to 16 kHz PCM (via ffmpeg), runs the recogniser,
// and outputs timestamped JSONL to stdout.
func newTranscribeCmd() *cobra.Command {
	var filePath string
	var outputFile string
	var refineModel string

	cmd := &cobra.Command{
		Use:   "transcribe",
		Short: "Transcribe an audio/video file to timestamped JSONL",
		Long: `On-demand file transcription.

Reads the given media file, decodes it to 16 kHz mono PCM via ffmpeg,
runs speech recognition (the --backend model, or the --refine-model path
below), and writes one JSON object per segment to stdout.
Optionally writes output to a file with --output (default: stdout only).

Requires ffmpeg to be installed and available on $PATH.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if filePath == "" {
				return fmt.Errorf("--file is required")
			}

			modelPath, _ := cmd.Flags().GetString("model")
			lang, _ := cmd.Flags().GetString("language")

			// Load language from config if flag not set.
			if lang == "" {
				if cfg := config.Load(); cfg != nil && cfg.Language != "" {
					lang = cfg.Language
				}
			}

			log.Println("[transcribe] initialising...")

			var segments []core.Segment
			refineModel = resolveRefineModel(refineModel)
			if refineModel != "" {
				// Offline path with the accurate model (and speaker names).
				ref, rerr := tcasr.NewRefiner(config.ExpandPath(refineModel), lang)
				if rerr != nil {
					return fmt.Errorf("refiner: %w", rerr)
				}
				defer ref.Close()
				fc := pipeline.FileConfig{
					Progress: func(done, total time.Duration) {
						log.Printf("[transcribe] %s / %s", fmtClock(done), fmtClock(total))
					},
				}
				if cfg := config.Load(); cfg != nil {
					fc.Corrector = core.NewCorrector(cfg.Corrections)
				}
				if sp, _ := cmd.Flags().GetBool("speakers"); sp || !cmd.Flags().Changed("speakers") {
					labeler, closeSp, lerr := newSpeakerLabeler(sp)
					if lerr != nil {
						return fmt.Errorf("speakers: %w", lerr)
					}
					defer closeSp()
					fc.Speakers = labeler
				}
				log.Printf("[transcribe] decoding %s", filePath)
				pcm, derr := core.DecodeFile(cmd.Context(), filePath)
				if derr != nil {
					return derr
				}
				vad, closeVAD := audio.OpenVAD()
				var terr error
				segments, terr = pipeline.TranscribeFile(cmd.Context(), pcm, vad, ref, fc)
				closeVAD()
				if terr != nil {
					return fmt.Errorf("transcription failed: %w", terr)
				}
			} else {
				// ── Initialise transcription engine ──────────────────
				resolved, err := resolveModelPath(modelPath)
				if err != nil {
					return fmt.Errorf("model not found: %w (check ~/.gleann/models or run 'gleann-plugin-sound setup')", err)
				}
				modelPath = resolved

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

				// ── Run transcription ──────────────────────────────
				log.Printf("[transcribe] processing file: %s", filePath)
				var terr error
				segments, terr = engine.TranscribeFile(cmd.Context(), filePath)
				if terr != nil {
					return fmt.Errorf("transcription failed: %w", terr)
				}
			}

			// ── Output as JSONL ────────────────────────────────────
			// Segments always go to stdout; a JSONL file is written only when
			// -o is given (never over the input's neighbours by default).
			stdoutEnc := json.NewEncoder(os.Stdout)
			var fileEnc *json.Encoder
			outPath := config.ExpandPath(outputFile)
			if outPath != "" {
				if info, statErr := os.Stat(outPath); (statErr == nil && info.IsDir()) || strings.HasSuffix(outPath, string(os.PathSeparator)) || filepath.Ext(outPath) == "" {
					if mkErr := os.MkdirAll(outPath, 0o755); mkErr != nil {
						return fmt.Errorf("failed to create output directory: %w", mkErr)
					}
					base := filepath.Base(filePath)
					outPath = filepath.Join(outPath, strings.TrimSuffix(base, filepath.Ext(base))+".jsonl")
				} else if mkErr := os.MkdirAll(filepath.Dir(outPath), 0o755); mkErr != nil {
					return fmt.Errorf("failed to create output directory: %w", mkErr)
				}
				f, err := os.Create(outPath)
				if err != nil {
					return fmt.Errorf("failed to create output file: %w", err)
				}
				defer f.Close()
				fileEnc = json.NewEncoder(f)
			}

			corr := core.NewCorrector(nil)
			if cfg := config.Load(); cfg != nil {
				corr = core.NewCorrector(cfg.Corrections)
			}
			for _, seg := range segments {
				seg.Text = corr.Apply(seg.Text)
				js := newJSONSegment(seg)
				if err := stdoutEnc.Encode(js); err != nil {
					return fmt.Errorf("failed to write JSON: %w", err)
				}
				if fileEnc != nil {
					if err := fileEnc.Encode(js); err != nil {
						return fmt.Errorf("failed to write to file: %w", err)
					}
				}
			}

			log.Printf("[transcribe] done — %d segments", len(segments))
			return nil
		},
	}

	cmd.Flags().StringVarP(&filePath, "file", "f", "", "Path to the audio/video file to transcribe")
	_ = cmd.MarkFlagRequired("file")
	cmd.Flags().StringVar(&refineModel, "refine-model", "",
		"Transcribe with this GGUF model (e.g. Qwen3-ASR-1.7B-Q8_0.gguf): speech is found with Silero VAD, split at speaker changes and transcribed piece by piece; far more accurate than --backend. Default: refine_model from sound.json; --refine-model=off uses --backend instead")
	cmd.Flags().Bool("speakers", false,
		"Name the speakers (on by default once voices are enrolled with 'speakers enroll'; --speakers=false disables)")
	cmd.Flags().StringVarP(&outputFile, "output", "o", "",
		"Also write the JSONL to this file or directory (default: stdout only)")
	cmd.Flags().String("language", "",
		"Language code for transcription (e.g. tr, en, de). Empty = auto-detect")

	return cmd
}

// jsonSegment is the JSONL output format for a single transcription segment.
type jsonSegment struct {
	StartMs int64  `json:"start_ms"`
	EndMs   int64  `json:"end_ms"`
	Text    string `json:"text"`
	// Partial marks a provisional line from 'listen --partials'.
	Partial bool `json:"partial,omitempty"`
	// Refined marks the better transcript of utterance Seq, produced after the
	// live line for it ('listen --refine-model').
	Refined bool `json:"refined,omitempty"`
	Seq     int  `json:"seq,omitempty"`
	// Part/Parts number the speaker turns of a refined utterance (when more than one).
	Part  int `json:"part,omitempty"`
	Parts int `json:"parts,omitempty"`
	// Speaker is who spoke ('listen' with enrolled voices; "?" = unknown).
	Speaker string `json:"speaker,omitempty"`
}

// newJSONSegment converts a core.Segment to the JSON output type.
func newJSONSegment(s core.Segment) jsonSegment {
	return jsonSegment{
		StartMs: s.Start.Milliseconds(),
		EndMs:   s.End.Milliseconds(),
		Text:    s.Text,
		Speaker: s.Speaker,
	}
}
