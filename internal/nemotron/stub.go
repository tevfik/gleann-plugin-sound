//go:build !onnx
// +build !onnx

package nemotron

import (
	"context"
	"fmt"
	"time"

	"github.com/tevfik/gleann-plugin-sound/internal/core"
)

func init() {
	core.RegisterBackend("nemotron", func(model string) (core.Transcriber, error) {
		return nil, fmt.Errorf("nemotron backend not available: rebuild with -tags onnx")
	})
}

// Engine is a no-op stub when building without the onnx tag.
type Engine struct{}

func (e *Engine) TranscribeStream(_ context.Context, _ []int16) (string, error) {
	return "", fmt.Errorf("nemotron stub: not compiled with onnx build tag")
}

func (e *Engine) TranscribeStreamSegments(_ context.Context, _ []int16) ([]core.Segment, error) {
	return nil, fmt.Errorf("nemotron stub: not compiled with onnx build tag")
}

func (e *Engine) TranscribeFile(_ context.Context, _ string) ([]core.Segment, error) {
	return nil, fmt.Errorf("nemotron stub: not compiled with onnx build tag")
}

func (e *Engine) SetLanguage(_ string) {}

func (e *Engine) NewLiveStream() (core.LiveStream, error) {
	return nil, fmt.Errorf("nemotron stub: not compiled with onnx build tag")
}

func (e *Engine) NewLiveStreamLang(string) (core.LiveStream, error) {
	return nil, fmt.Errorf("nemotron stub: not compiled with onnx build tag")
}

func (e *Engine) Close() error { return nil }

var _ = time.Second
