//go:build !onnx
// +build !onnx

// Package onnx provides an ONNX Runtime-based Whisper transcription engine.
//
// This stub is compiled when the "onnx" build tag is NOT set.
// It registers a backend that returns a clear error directing users to
// rebuild with the correct tag.
package onnx

import (
	"context"
	"fmt"
	"time"

	"github.com/tevfik/gleann-sound/internal/core"
)

func init() {
	core.RegisterBackend("onnx", func(model string) (core.Transcriber, error) {
		return nil, fmt.Errorf("onnx backend not available: rebuild with -tags onnx")
	})
}

// Engine is a no-op stub when building without the onnx tag.
type Engine struct{}

func (e *Engine) TranscribeStream(_ context.Context, _ []int16) (string, error) {
	return "", fmt.Errorf("onnx stub: not compiled with onnx build tag")
}

func (e *Engine) TranscribeStreamSegments(_ context.Context, _ []int16) ([]core.Segment, error) {
	return nil, fmt.Errorf("onnx stub: not compiled with onnx build tag")
}

func (e *Engine) TranscribeFile(_ context.Context, _ string) ([]core.Segment, error) {
	return nil, fmt.Errorf("onnx stub: not compiled with onnx build tag")
}

func (e *Engine) SetLanguage(_ string) {}

func (e *Engine) Close() error { return nil }

// Ensure unused import suppression.
var _ = time.Second
