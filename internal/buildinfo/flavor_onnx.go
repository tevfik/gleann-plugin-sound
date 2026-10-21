//go:build onnx && !whisper

package buildinfo

// Flavor names the compiled-in backends; it selects the matching release asset.
const Flavor = "onnx"
