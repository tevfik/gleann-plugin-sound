package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"

	"github.com/tevfik/gleann-plugin-sound/internal/core"
	"github.com/tevfik/gleann-plugin-sound/internal/evaluate"

	// Register backends
	_ "github.com/tevfik/gleann-plugin-sound/internal/nemotron"
	_ "github.com/tevfik/gleann-plugin-sound/internal/onnx"
	_ "github.com/tevfik/gleann-plugin-sound/internal/whisper"
)

type GroundTruth struct {
	Text string `json:"text"`
}

func main() {
	var backend, modelPath, testDir string

	if len(os.Args) >= 3 {
		backend = os.Args[1]
		modelPath = os.Args[2]
	}

	for i := 3; i < len(os.Args); i++ {
		if os.Args[i] == "--dir" && i+1 < len(os.Args) {
			testDir = os.Args[i+1]
			i++
		}
	}

	if backend == "" || modelPath == "" {
		log.Fatal("Usage: go run test/benchmark/evaluate_dataset.go <backend> <model_path> [--dir <path>]")
	}

	if testDir == "" {
		testDir = "test/testdata"
	}

	engine, err := core.NewTranscriber(backend, modelPath)
	if err != nil {
		log.Fatalf("Failed to load model: %v", err)
	}
	defer engine.Close()

	engine.SetLanguage("tr")

	files, err := os.ReadDir(testDir)
	if err != nil {
		log.Fatalf("Failed to read testdir: %v", err)
	}

	var totalWER, totalCER float64
	var count int

	fmt.Println("=== RUNNING DATASET EVALUATION ===")

	for _, f := range files {
		ext := strings.ToLower(filepath.Ext(f.Name()))
		if ext == ".wav" || ext == ".mp3" {
			audioPath := filepath.Join(testDir, f.Name())
			jsonlPath := strings.TrimSuffix(audioPath, ext) + ".jsonl"

			if _, err := os.Stat(jsonlPath); err != nil {
				continue // No ground truth
			}

			jsonlBytes, err := os.ReadFile(jsonlPath)
			if err != nil {
				continue
			}

			var gt GroundTruth
			if err := json.Unmarshal(jsonlBytes, &gt); err != nil {
				log.Printf("Failed to parse jsonl for %s: %v", f.Name(), err)
				continue
			}

			segments, err := engine.TranscribeFile(context.Background(), audioPath)
			if err != nil {
				log.Printf("Failed to transcribe %s: %v", f.Name(), err)
				continue
			}

			var hypText string
			for _, s := range segments {
				hypText += s.Text + " "
			}
			hypText = strings.TrimSpace(hypText)

			wer := evaluate.CalculateWER(gt.Text, hypText)
			cer := evaluate.CalculateCER(gt.Text, hypText)

			totalWER += wer
			totalCER += cer
			count++

			fmt.Printf("File: %s\n", f.Name())
			fmt.Printf("REF : %s\n", gt.Text)
			fmt.Printf("HYP : %s\n", hypText)
			fmt.Printf("WER : %.2f%%, CER: %.2f%%\n", wer*100, cer*100)
			fmt.Println(strings.Repeat("-", 40))
		}
	}

	if count > 0 {
		avgWER := totalWER / float64(count)
		avgCER := totalCER / float64(count)
		fmt.Printf("\n=== SUMMARY ===\n")
		fmt.Printf("Total Files Evaluated: %d\n", count)
		fmt.Printf("Average WER: %.2f%%\n", avgWER*100)
		fmt.Printf("Average CER: %.2f%%\n", avgCER*100)
	} else {
		fmt.Println("No test files with ground truth found.")
	}
}
