//go:build !onnx
// +build !onnx

package main

import (
	"fmt"

	"github.com/tevfik/gleann-plugin-sound/internal/core"
)

var errNoONNX = fmt.Errorf("speaker identification needs the ONNX build: rebuild with -tags onnx")

func openEmbedder() (embedCloser, error) { return nil, errNoONNX }

func newSpeakerLabeler(force bool) (core.SpeakerLabeler, func(), error) {
	if force {
		return nil, nil, errNoONNX
	}
	return nil, func() {}, nil
}
