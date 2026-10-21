//go:build !onnx
// +build !onnx

package audio

import (
	"fmt"

	"github.com/tevfik/gleann-plugin-sound/internal/core"
)

// SileroVAD is a stub when building without the onnx tag.
type SileroVAD struct{}

// NewSileroVAD returns an error when ONNX Runtime is not available.
func NewSileroVAD(modelPath string) (*SileroVAD, error) {
	return nil, fmt.Errorf("silero vad: ONNX runtime not available (rebuild with -tags onnx)")
}

// OpenVAD returns the energy detector when the ONNX build is not available.
func OpenVAD() (core.VADProvider, func()) { return DefaultVAD(), func() {} }
