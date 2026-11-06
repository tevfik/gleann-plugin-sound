package config

import "testing"

func TestBackendForModel(t *testing.T) {
	cases := []struct {
		e    ModelEntry
		want string
	}{
		{ModelEntry{Name: "small-q5_1", Path: "/m/ggml-small-q5_1.bin"}, "whisper"},
		{ModelEntry{Name: "onnx-base", Path: "/m/whisper-base-onnx"}, "onnx"},
		{ModelEntry{Name: "nemotron-3.5-asr-int4", Path: "/m/nemotron-3.5-asr-int4-onnx"}, "nemotron"},
		{ModelEntry{Name: "nemotron-3.5-asr-q8-gguf", Path: "/m/nemotron-3.5-asr-streaming-0.6b-Q8_0.gguf"}, "tc"},
		{ModelEntry{Name: "qwen3-asr-1.7b-q8-gguf", Path: "/m/Qwen3-ASR-1.7B-Q8_0.gguf"}, ""},
	}
	for _, c := range cases {
		if got := BackendForModel(c.e); got != c.want {
			t.Errorf("BackendForModel(%s) = %q, want %q", c.e.Name, got, c.want)
		}
	}
}
