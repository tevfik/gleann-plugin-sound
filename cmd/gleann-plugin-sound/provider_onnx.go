//go:build onnx
// +build onnx

package main

import (
	"github.com/tevfik/gleann-plugin-sound/internal/nemotron"
	"github.com/tevfik/gleann-plugin-sound/internal/onnx"
)

// setONNXProvider configures the ONNX Runtime execution provider.
// Only effective when compiled with the onnx build tag.
func setONNXProvider(provider string) {
	onnx.SetExecutionProvider(provider)
	nemotron.SetExecutionProvider(provider)
}
