//go:build onnx
// +build onnx

package main

import (
	"fmt"
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

func openEmbedder() (embedCloser, error) {
	e, err := speaker.NewEmbedder(speakerModelPath(config.Load()))
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
