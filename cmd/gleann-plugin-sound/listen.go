package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/cobra"
	"github.com/tevfik/gleann-plugin-sound/internal/audio"
	"github.com/tevfik/gleann-plugin-sound/internal/config"
	"github.com/tevfik/gleann-plugin-sound/internal/core"
	"github.com/tevfik/gleann-plugin-sound/internal/pipeline"
	"github.com/tevfik/gleann-plugin-sound/internal/tcasr"
)

// newListenCmd creates the "listen" subcommand (Live CLI Stream).
func newListenCmd() *cobra.Command {
	var outputFile string
	var sourceFlag string
	var partials bool
	var refineModel string

	cmd := &cobra.Command{
		Use:   "listen",
		Short: "Live audio transcription streamed as JSON to stdout",
		Long: `Live CLI streaming.

Captures audio from the selected source (microphone, speaker/loopback, or both)
and prints one JSON line per utterance. With a streaming backend (tc or nemotron) the model is
fed audio continuously (cache-aware streaming, ~0.6 s chunks), so text follows
speech with sub-second latency; an utterance ends after a pause. --partials also
prints provisional lines as words arrive.

The whisper and onnx backends use a sliding-window pipeline (3-10 s windows, with VAD and
deduplication).

Audio sources:
  mic      — Default microphone input (default)
  speaker  — System audio output (loopback/desktop audio)
  both     — Microphone and speaker simultaneously

Press Ctrl+C to stop.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			modelPath, _ := cmd.Flags().GetString("model")
			lang, _ := cmd.Flags().GetString("language")

			// Load language from config if flag not set.
			if lang == "" {
				if cfg := config.Load(); cfg != nil && cfg.Language != "" {
					lang = cfg.Language
				}
			}

			resolved, err := resolveModelPath(modelPath)
			if err != nil {
				return fmt.Errorf("model not found: %w (check ~/.gleann/models or run 'gleann-plugin-sound setup')", err)
			}
			modelPath = resolved

			log.Println("[listen] initialising...")

			// ── Initialise transcription engine ───────────────────
			backend, _ := cmd.Flags().GetString("backend")
			log.Printf("[listen] loading model: %s (backend: %s)", modelPath, backend)
			engine, err := core.NewTranscriber(backend, modelPath)
			if err != nil {
				return fmt.Errorf("failed to load model: %w", err)
			}
			defer engine.Close()

			if lang != "" {
				engine.SetLanguage(lang)
			}

			// Native streaming (Nemotron) is preferred; other backends fall
			// back to the sliding-window pipeline.
			liveEngine, isLive := engine.(core.LiveTranscriber)
			streamEngine, isWindowed := engine.(core.StreamingTranscriber)
			if !isLive && !isWindowed {
				return fmt.Errorf("backend %q does not support streaming transcription", backend)
			}

			// ── Audio source selection ─────────────────────────────
			src := sourceFlag
			if src == "" {
				if cfg := config.Load(); cfg != nil && cfg.AudioSource != "" {
					src = cfg.AudioSource
				} else {
					src = "mic"
				}
			}
			audioSource, err := audio.ParseAudioSource(src)
			if err != nil {
				return err
			}
			log.Printf("[listen] audio source: %s", audioSource)

			capturer := audio.NewMalgoCapturerWithSource(audioSource)
			vad, closeVAD := audio.OpenVAD()
			defer closeVAD()

			// ── Signal handling ────────────────────────────────────
			ctx, cancel := context.WithCancel(cmd.Context())
			defer cancel()

			sigCh := make(chan os.Signal, 1)
			signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
			go func() {
				<-sigCh
				log.Println("[listen] shutting down…")
				cancel()
			}()

			// ── Output setup ───────────────────────────────────────
			stdoutEnc := json.NewEncoder(os.Stdout)
			var outFile *os.File
			var outIsTxt bool
			if outputFile != "" {
				outputFile = expandOutputPath(outputFile)
				isDir := strings.HasSuffix(outputFile, string(os.PathSeparator)) || filepath.Ext(outputFile) == ""
				if info, err := os.Stat(outputFile); err == nil && info.IsDir() {
					isDir = true
				}
				if isDir {
					if err := os.MkdirAll(outputFile, 0o755); err != nil {
						return fmt.Errorf("failed to create output directory: %w", err)
					}
					stamp := time.Now().Format("2006-01-02_15-04-05")
					outputFile = filepath.Join(outputFile, stamp+".txt")
				} else {
					if err := os.MkdirAll(filepath.Dir(outputFile), 0o755); err != nil {
						return fmt.Errorf("failed to create output directory: %w", err)
					}
				}
				f, err := os.Create(outputFile)
				if err != nil {
					return fmt.Errorf("failed to create output file: %w", err)
				}
				defer f.Close()
				outFile = f
				outIsTxt = strings.HasSuffix(outputFile, ".txt")
				log.Printf("[listen] output file: %s", outputFile)
			}

			writeResult := func(result core.StreamResult, who string) {
				seg := core.Segment{
					Start: result.Start,
					End:   result.End,
					Text:  result.Text,
				}
				js := newJSONSegment(seg)
				js.Seq = result.WindowSeq
				js.Speaker = who
				_ = stdoutEnc.Encode(js)
				if outFile != nil {
					if outIsTxt {
						line := strings.TrimSpace(result.Text)
						if who != "" {
							line = who + ": " + line
						}
						fmt.Fprintf(outFile, "%s\n", line)
					} else {
						_ = json.NewEncoder(outFile).Encode(js)
					}
				}
			}

			// ── Start audio capture into channel ───────────────────
			// Buffer ~30 seconds of audio (30s × 16kHz / 480 frames ≈ 1000).
			// This gives the pipeline headroom when inference is slow.
			audioCh := make(chan []int16, 1024)
			var dropCount int64
			var lastDropLog time.Time
			err = capturer.Start(ctx, func(pcmData []int16) {
				// Copy data to avoid malgo buffer reuse issues.
				chunk := make([]int16, len(pcmData))
				copy(chunk, pcmData)
				select {
				case audioCh <- chunk:
				default:
					dropCount++
					// Throttle warning: log at most once per second.
					if now := time.Now(); now.Sub(lastDropLog) > time.Second {
						log.Printf("[listen] WARNING: dropped %d audio frames (pipeline backed up)", dropCount)
						dropCount = 0
						lastDropLog = now
					}
				}
			})
			if err != nil {
				return fmt.Errorf("failed to start audio capture: %w", err)
			}
			defer func() { _ = capturer.Stop() }()

			log.Println("[listen] streaming pipeline active — listening… (Ctrl+C to stop)")

			// ── Run the streaming pipeline ─────────────────────────
			if isLive {
				stream, serr := liveEngine.NewLiveStream()
				if serr != nil {
					return fmt.Errorf("failed to open live stream: %w", serr)
				}
				defer stream.Close()
				liveCfg := pipeline.DefaultLiveConfig()
				var refiner *tcasr.Refiner
				if cfg := config.Load(); cfg != nil {
					liveCfg.Corrector = core.NewCorrector(cfg.Corrections)
				}
				refineModel = resolveRefineModel(refineModel)
				if refineModel != "" {
					var rerr error
					if refiner, rerr = tcasr.NewRefiner(config.ExpandPath(refineModel), lang); rerr != nil {
						return fmt.Errorf("refiner: %w", rerr)
					}
				}
				if sp, _ := cmd.Flags().GetBool("speakers"); sp || !cmd.Flags().Changed("speakers") {
					labeler, closeSp, lerr := newSpeakerLabeler(sp)
					if lerr != nil {
						return fmt.Errorf("speakers: %w", lerr)
					}
					defer closeSp()
					liveCfg.Speakers = labeler
				}
				log.Println("[listen] native streaming: words appear ~1 s after they are spoken")
				if refiner != nil {
					defer refiner.Close()
					liveCfg.Refiner = refiner
					log.Println("[listen] refinement on: finished utterances are re-transcribed in the background")
				}
				err = pipeline.RunLive(ctx, stream, vad, audioCh, liveCfg, func(ev pipeline.LiveEvent) {
					if ev.Refined {
						_ = stdoutEnc.Encode(jsonSegment{
							StartMs: ev.Start.Milliseconds(), EndMs: ev.End.Milliseconds(),
							Text: ev.Text, Refined: true, Seq: ev.Seq + 1, Speaker: ev.Speaker, Part: ev.Part, Parts: ev.Parts,
						})
						if outFile != nil && !outIsTxt {
							_ = json.NewEncoder(outFile).Encode(jsonSegment{
								StartMs: ev.Start.Milliseconds(), EndMs: ev.End.Milliseconds(),
								Text: ev.Text, Refined: true, Seq: ev.Seq + 1, Speaker: ev.Speaker, Part: ev.Part, Parts: ev.Parts,
							})
						}
						return
					}
					if !ev.Final {
						if partials && (ev.Pending != "" || ev.Delta != "") {
							_ = stdoutEnc.Encode(jsonSegment{
								StartMs: ev.Start.Milliseconds(), EndMs: ev.End.Milliseconds(),
								Text: strings.TrimSpace(ev.Text + " " + ev.Pending), Partial: true,
							})
						}
						return
					}
					if ev.Text == "" { // live model heard nothing; only the refined line follows
						return
					}
					writeResult(core.StreamResult{Text: ev.Text, Start: ev.Start, End: ev.End, IsFinal: true, WindowSeq: ev.Seq + 1}, ev.Speaker)
				})
			} else {
				// ONNX CPU inference is much slower — use larger windows.
				pipeCfg := pipeline.DefaultConfig()
				if backend == "onnx" {
					pipeCfg = pipeline.ONNXConfig()
				}
				pipe := pipeline.NewStreamingPipeline(streamEngine, vad, pipeCfg)
				err = pipe.Run(ctx, audioCh, func(result core.StreamResult) {
					writeResult(result, "")
				})
			}

			if outFile != nil {
				_ = outFile.Sync()
			}

			// context.Canceled is normal shutdown, not an error.
			if err == context.Canceled {
				return nil
			}
			return err
		},
	}

	cmd.Flags().StringVarP(&outputFile, "output", "o", "",
		"Write transcription output to this file (in addition to stdout)")
	cmd.Flags().StringVarP(&sourceFlag, "source", "s", "",
		"Audio source: mic (default), speaker (loopback/desktop audio), both")
	cmd.Flags().String("language", "",
		"Language code for transcription (e.g. tr, en, de). Empty = auto-detect")
	cmd.Flags().Bool("speakers", false,
		"Label each utterance with its speaker. On by default once voices are enrolled ('speakers enroll'); this flag also numbers unenrolled voices, --speakers=false turns it off")
	cmd.Flags().StringVar(&refineModel, "refine-model", "",
		"GGUF model (e.g. Qwen3-ASR-1.7B-Q8_0.gguf) that re-transcribes each finished utterance in the background; arrives as \"refined\":true lines")
	cmd.Flags().BoolVar(&partials, "partials", false,
		"Also print provisional lines (\"partial\":true) while an utterance is still being spoken (tc, nemotron)")

	return cmd
}

// expandOutputPath expands ~ and resolves the output path.
func expandOutputPath(p string) string {
	return config.ExpandPath(p)
}
