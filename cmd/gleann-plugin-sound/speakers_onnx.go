//go:build onnx
// +build onnx

package main

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/tevfik/gleann-plugin-sound/internal/config"
	"github.com/tevfik/gleann-plugin-sound/internal/core"
	"github.com/tevfik/gleann-plugin-sound/internal/speaker"
)

func speakerModelPath(cfg *config.Config) string {
	if cfg != nil && cfg.SpeakerModel != "" {
		return config.ExpandPath(cfg.SpeakerModel)
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".gleann", "models", "nemo_en_titanet_small.onnx")
}

// Voice-print model published with sherpa-onnx (NVIDIA TitaNet-small, ONNX).
const (
	speakerModelURL    = "https://github.com/k2-fsa/sherpa-onnx/releases/download/speaker-recongition-models/nemo_en_titanet_small.onnx"
	speakerModelSHA256 = "ad4a1802485d8b34c722d2a9d04249662f2ece5d28a7a039063ca22f515a789e"
)

// ensureSpeakerModel downloads the default voice-print model when it is missing.
// A model path set in the config is never fetched: it is the user's own file.
func ensureSpeakerModel(path string, custom bool) error {
	if _, err := os.Stat(path); err == nil || custom {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "[speakers] downloading the voice model (40 MB) to %s\n", path)
	var last int
	err := config.HTTPDownloadWithProgress(speakerModelURL, path, func(read, total int64, _ float64) {
		if total > 0 {
			if pct := int(read * 100 / total); pct/10 != last/10 {
				last = pct
				fmt.Fprintf(os.Stderr, "[speakers]   %d%%\n", pct)
			}
		}
	})
	if err != nil {
		_ = os.Remove(path)
		return fmt.Errorf("download voice model: %w (get it from %s)", err, speakerModelURL)
	}
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return err
	}
	if sum := hex.EncodeToString(h.Sum(nil)); sum != speakerModelSHA256 {
		_ = os.Remove(path)
		return fmt.Errorf("voice model checksum mismatch (got %s)", sum)
	}
	return nil
}

func openEmbedder() (embedCloser, error) {
	cfg := config.Load()
	path := speakerModelPath(cfg)
	if err := ensureSpeakerModel(path, cfg != nil && cfg.SpeakerModel != ""); err != nil {
		return nil, err
	}
	e, err := speaker.NewEmbedder(path)
	if err != nil {
		return nil, err
	}
	return e, nil
}

// newSpeakerLabeler returns a labeler for finished utterances, or nil when
// speaker identification is off. It is on when voices are enrolled or when
// force is set (then unenrolled voices are numbered "Speaker N").
func newSpeakerLabeler(force bool) (core.SpeakerLabeler, func(), error) {
	reg, err := speaker.LoadRegistry(speaker.DefaultPath())
	if err != nil {
		return nil, nil, err
	}
	if reg.Len() == 0 && !force {
		return nil, func() {}, nil
	}
	emb, err := openEmbedder()
	if err != nil {
		return nil, nil, err
	}
	var th float64
	if cfg := config.Load(); cfg != nil {
		th = cfg.SpeakerThreshold
	}
	if reg.Len() > 0 {
		fmt.Fprintf(os.Stderr, "[speakers] identifying: %s\n", reg.Describe())
	} else {
		fmt.Fprintln(os.Stderr, "[speakers] no voices enrolled: numbering speakers as they appear")
	}
	return &speaker.Labeler{Emb: emb, Reg: reg, Threshold: th}, func() { _ = emb.Close() }, nil
}
