//go:build whisper && onnx

package buildinfo

// Flavor names the compiled-in backends; it selects the matching release asset.
const Flavor = "whisper-onnx"
