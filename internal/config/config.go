// Package config manages gleann-plugin-sound configuration stored in ~/.gleann/sound.json.
//
// If the config file does not exist, the application falls back to CLI flags.
// The TUI wizard creates and updates this file.
package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// Config holds the persisted gleann-plugin-sound configuration.
type Config struct {
	// Model settings.
	DefaultModel string `json:"default_model"`          // e.g. "~/.gleann/models/ggml-small.bin"
	Language      string `json:"language,omitempty"`      // e.g. "tr", "" = auto-detect

	// Hotkey settings (dictate mode).
	Hotkey string `json:"hotkey,omitempty"` // e.g. "ctrl+shift+space"

	// Installed models metadata.
	Models []ModelEntry `json:"models,omitempty"`

	// Backend selection.
	Backend string `json:"backend,omitempty"` // "whisper" (default) or "onnx"

	// Execution provider for ONNX backend: "auto" (default), "cuda", "cpu".
	// "auto" tries CUDA first, falls back to CPU.
	ExecutionProvider string `json:"execution_provider,omitempty"`

	// Audio source: "mic" (default), "speaker" (loopback), "both".
	AudioSource string `json:"audio_source,omitempty"`

	// Output directory for transcription results.
	OutputDir string `json:"output_dir,omitempty"` // e.g. "~/transcriptions"

	// gRPC settings (optional — enables gRPC server alongside dictation).
	GRPCAddr string `json:"grpc_addr,omitempty"` // e.g. "localhost:50051"

	// Install state.
	InstallPath           string `json:"install_path,omitempty"`
	CompletionsInstalled  bool   `json:"completions_installed,omitempty"`
	InputGroupSetup       bool   `json:"input_group_setup,omitempty"`
	ONNXRuntimeSystemWide bool   `json:"onnx_runtime_system_wide,omitempty"`
	DaemonEnabled         bool   `json:"daemon_enabled,omitempty"`

	// Flag indicating setup was completed.
	Completed bool `json:"completed"`
}

// ModelEntry describes a downloaded Whisper model.
type ModelEntry struct {
	Name     string `json:"name"`      // e.g. "small"
	Path     string `json:"path"`      // absolute path
	Size     string `json:"size"`      // human-readable
	Language string `json:"language"`  // "multilingual" or "en"
}

// WhisperModel describes an available model for download.
// For single-file models (GGML, Silero VAD), URL and FileName are used.
// For multi-file bundles (ONNX), BundleFiles lists relative paths and URLs,
// and FileName is the subdirectory name (e.g. "whisper-base.en-onnx").
type WhisperModel struct {
	Name        string // e.g. "tiny", "base", "small"
	DisplayName string // e.g. "Tiny (75 MB)"
	FileName    string // e.g. "ggml-tiny.bin" or "whisper-base.en-onnx" (directory)
	Size        string // e.g. "75 MB"
	Multilingual bool
	URL         string // single-file download URL (empty for bundles)

	// BundleFiles lists files to download into a subdirectory.
	// Each entry maps a relative filename to its download URL.
	// When non-nil, this is a multi-file model (e.g. ONNX encoder+decoder).
	BundleFiles []BundleFile `json:"-"`
}

// BundleFile describes a single file within a multi-file model bundle.
type BundleFile struct {
	Name string // relative filename, e.g. "encoder.onnx"
	URL  string // download URL
}

// IsBundle returns true if this model requires multi-file download.
func (m WhisperModel) IsBundle() bool {
	return len(m.BundleFiles) > 0
}

// AvailableModels returns the list of Whisper models available for download.
// Includes both full-precision (f16) and quantized (q5, q8) variants.
// Quantized models are significantly smaller and faster with minimal quality loss.
func AvailableModels() []WhisperModel {
	base := "https://huggingface.co/ggerganov/whisper.cpp/resolve/main"
	return []WhisperModel{
		// ── Full-precision models ──────────────────────────────
		{Name: "tiny", DisplayName: "Tiny — 75 MB, fastest", FileName: "ggml-tiny.bin", Size: "75 MB", Multilingual: true, URL: base + "/ggml-tiny.bin"},
		{Name: "tiny.en", DisplayName: "Tiny English — 75 MB, fastest (EN only)", FileName: "ggml-tiny.en.bin", Size: "75 MB", Multilingual: false, URL: base + "/ggml-tiny.en.bin"},
		{Name: "base", DisplayName: "Base — 142 MB, fast", FileName: "ggml-base.bin", Size: "142 MB", Multilingual: true, URL: base + "/ggml-base.bin"},
		{Name: "base.en", DisplayName: "Base English — 142 MB, fast (EN only)", FileName: "ggml-base.en.bin", Size: "142 MB", Multilingual: false, URL: base + "/ggml-base.en.bin"},
		{Name: "small", DisplayName: "Small — 466 MB, good balance ★", FileName: "ggml-small.bin", Size: "466 MB", Multilingual: true, URL: base + "/ggml-small.bin"},
		{Name: "small.en", DisplayName: "Small English — 466 MB (EN only)", FileName: "ggml-small.en.bin", Size: "466 MB", Multilingual: false, URL: base + "/ggml-small.en.bin"},
		{Name: "medium", DisplayName: "Medium — 1.5 GB, high quality", FileName: "ggml-medium.bin", Size: "1.5 GB", Multilingual: true, URL: base + "/ggml-medium.bin"},
		{Name: "medium.en", DisplayName: "Medium English — 1.5 GB (EN only)", FileName: "ggml-medium.en.bin", Size: "1.5 GB", Multilingual: false, URL: base + "/ggml-medium.en.bin"},
		{Name: "large-v3-turbo", DisplayName: "Large V3 Turbo — 1.6 GB, best quality", FileName: "ggml-large-v3-turbo.bin", Size: "1.6 GB", Multilingual: true, URL: base + "/ggml-large-v3-turbo.bin"},

		// ── Quantized models (smaller & faster) ─────────────
		{Name: "tiny-q5_1", DisplayName: "Tiny Q5 — 31 MB, fastest quantized", FileName: "ggml-tiny-q5_1.bin", Size: "31 MB", Multilingual: true, URL: base + "/ggml-tiny-q5_1.bin"},
		{Name: "tiny.en-q5_1", DisplayName: "Tiny English Q5 — 31 MB (EN only)", FileName: "ggml-tiny.en-q5_1.bin", Size: "31 MB", Multilingual: false, URL: base + "/ggml-tiny.en-q5_1.bin"},
		{Name: "base-q5_1", DisplayName: "Base Q5 — 57 MB, fast quantized", FileName: "ggml-base-q5_1.bin", Size: "57 MB", Multilingual: true, URL: base + "/ggml-base-q5_1.bin"},
		{Name: "base.en-q5_1", DisplayName: "Base English Q5 — 57 MB (EN only)", FileName: "ggml-base.en-q5_1.bin", Size: "57 MB", Multilingual: false, URL: base + "/ggml-base.en-q5_1.bin"},
		{Name: "small-q5_1", DisplayName: "Small Q5 — 181 MB, great balance ★★", FileName: "ggml-small-q5_1.bin", Size: "181 MB", Multilingual: true, URL: base + "/ggml-small-q5_1.bin"},
		{Name: "small.en-q5_1", DisplayName: "Small English Q5 — 181 MB (EN only)", FileName: "ggml-small.en-q5_1.bin", Size: "181 MB", Multilingual: false, URL: base + "/ggml-small.en-q5_1.bin"},
		{Name: "medium-q5_0", DisplayName: "Medium Q5 — 514 MB, quality quantized", FileName: "ggml-medium-q5_0.bin", Size: "514 MB", Multilingual: true, URL: base + "/ggml-medium-q5_0.bin"},
		{Name: "medium.en-q5_0", DisplayName: "Medium English Q5 — 514 MB (EN only)", FileName: "ggml-medium.en-q5_0.bin", Size: "514 MB", Multilingual: false, URL: base + "/ggml-medium.en-q5_0.bin"},
		{Name: "large-v3-turbo-q5_0", DisplayName: "Large V3 Turbo Q5 — 547 MB, best quantized ★★★", FileName: "ggml-large-v3-turbo-q5_0.bin", Size: "547 MB", Multilingual: true, URL: base + "/ggml-large-v3-turbo-q5_0.bin"},
		{Name: "large-v3-turbo-q8_0", DisplayName: "Large V3 Turbo Q8 — 834 MB, near-lossless", FileName: "ggml-large-v3-turbo-q8_0.bin", Size: "834 MB", Multilingual: true, URL: base + "/ggml-large-v3-turbo-q8_0.bin"},
	}
}

// AvailableONNXModels returns ONNX Runtime whisper models available for download.
// Each model is a bundle of encoder.onnx, decoder.onnx, and tokenizer.json.
//
// Source: onnx-community on HuggingFace (pre-exported ONNX models).
// Files are renamed during download to match what the engine expects:
//
//	encoder_model.onnx → encoder.onnx
//	decoder_model.onnx → decoder.onnx  (non-merged, no KV cache)
//	tokenizer.json     → tokenizer.json
//
// We use decoder_model.onnx (NOT decoder_model_merged.onnx) because the
// merged model requires a use_cache_branch boolean + past_key_values tensors
// which adds complexity. The non-merged model is simpler: just input_ids +
// encoder_hidden_states → logits.
func AvailableONNXModels() []WhisperModel {
	makeBundle := func(slug, name, display, size string, multi bool) WhisperModel {
		repo := fmt.Sprintf("https://huggingface.co/onnx-community/whisper-%s/resolve/main", slug)
		return WhisperModel{
			Name:         name,
			DisplayName:  display,
			FileName:     "whisper-" + slug + "-onnx",
			Size:         size,
			Multilingual: multi,
			BundleFiles: []BundleFile{
				{Name: "encoder.onnx", URL: repo + "/onnx/encoder_model.onnx"},
				{Name: "decoder.onnx", URL: repo + "/onnx/decoder_model.onnx"},
				{Name: "tokenizer.json", URL: repo + "/tokenizer.json"},
			},
		}
	}

	return []WhisperModel{
		makeBundle("tiny.en", "onnx-tiny.en", "[ONNX] Tiny English — ~150 MB", "150 MB", false),
		makeBundle("tiny", "onnx-tiny", "[ONNX] Tiny — ~150 MB", "150 MB", true),
		makeBundle("base.en", "onnx-base.en", "[ONNX] Base English — ~290 MB", "290 MB", false),
		makeBundle("base", "onnx-base", "[ONNX] Base — ~290 MB", "290 MB", true),
		makeBundle("small.en", "onnx-small.en", "[ONNX] Small English — ~950 MB", "950 MB", false),
		makeBundle("small", "onnx-small", "[ONNX] Small — ~950 MB", "950 MB", true),
		makeBundle("medium.en", "onnx-medium.en", "[ONNX] Medium English — ~3 GB", "3 GB", false),
		makeBundle("medium", "onnx-medium", "[ONNX] Medium — ~3 GB", "3 GB", true),
	}
}

// AvailableNemotronModels returns the Nemotron ASR models available for download.
func AvailableNemotronModels() []WhisperModel {
	repo := "https://huggingface.co/onnx-community/nemotron-3.5-asr-streaming-0.6b-onnx-int4/resolve/main"
	return []WhisperModel{
		{
			Name:         "nemotron-3.5-asr-int4",
			DisplayName:  "[Nemotron ASR] Causal Streaming ASR tr/en (INT4) — ~450 MB",
			FileName:     "nemotron-3.5-asr-int4-onnx",
			Size:         "450 MB",
			Multilingual: true,
			BundleFiles: []BundleFile{
				{Name: "encoder.onnx", URL: repo + "/encoder.onnx"},
				{Name: "encoder.onnx.data", URL: repo + "/encoder.onnx.data"},
				{Name: "decoder.onnx", URL: repo + "/decoder.onnx"},
				{Name: "decoder.onnx.data", URL: repo + "/decoder.onnx.data"},
				{Name: "joint.onnx", URL: repo + "/joint.onnx"},
				{Name: "joint.onnx.data", URL: repo + "/joint.onnx.data"},
				{Name: "vocab.txt", URL: repo + "/vocab.txt"},
				{Name: "tokenizer.json", URL: repo + "/tokenizer.json"},
			},
		},
	}
}


// DefaultDir returns the gleann config directory: ~/.gleann
func DefaultDir() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".gleann")
}

// ModelsDir returns ~/.gleann/models
func ModelsDir() string {
	return filepath.Join(DefaultDir(), "models")
}

// LibDir returns ~/.gleann/lib — used for auto-downloaded runtime libraries.
func LibDir() string {
	return filepath.Join(DefaultDir(), "lib")
}

// ConfigPath returns ~/.gleann/sound.json
func ConfigPath() string {
	return filepath.Join(DefaultDir(), "sound.json")
}

// Load reads the config from disk. Returns nil if not found.
func Load() *Config {
	data, err := os.ReadFile(ConfigPath())
	if err != nil {
		return nil
	}
	var cfg Config
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil
	}
	// Expand paths.
	if cfg.DefaultModel != "" {
		cfg.DefaultModel = ExpandPath(cfg.DefaultModel)
	}
	if cfg.OutputDir != "" {
		cfg.OutputDir = ExpandPath(cfg.OutputDir)
	}
	for i := range cfg.Models {
		if cfg.Models[i].Path != "" {
			cfg.Models[i].Path = ExpandPath(cfg.Models[i].Path)
		}
	}
	return &cfg
}

// Save persists the configuration to disk.
func Save(cfg *Config) error {
	dir := DefaultDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(ConfigPath(), data, 0o644)
}

// ExpandPath expands ~ prefix to the user's home directory.
func ExpandPath(p string) string {
	if p == "" {
		return p
	}
	if p == "~" {
		home, _ := os.UserHomeDir()
		return home
	}
	if strings.HasPrefix(p, "~/") || strings.HasPrefix(p, "~\\") {
		home, _ := os.UserHomeDir()
		p = filepath.Join(home, p[2:])
	}
	if runtime.GOOS == "windows" {
		p = strings.ReplaceAll(p, "/", "\\")
	}
	return filepath.Clean(p)
}

// ModelPath returns the full path to a model file in the models directory.
func ModelPath(filename string) string {
	return filepath.Join(ModelsDir(), filename)
}

// IsModelDownloaded checks if a model file (or bundle directory) exists in the models directory.
func IsModelDownloaded(filename string) bool {
	info, err := os.Stat(ModelPath(filename))
	if err != nil {
		return false
	}
	// For bundle directories, check that at least encoder.onnx exists inside.
	if info.IsDir() {
		_, err := os.Stat(filepath.Join(ModelPath(filename), "encoder.onnx"))
		return err == nil
	}
	return true
}

// SileroVADModel returns the Silero VAD model metadata for download.
func SileroVADModel() WhisperModel {
	return WhisperModel{
		Name:        "silero-vad",
		DisplayName: "Silero VAD — 2 MB (neural speech detection)",
		FileName:    "silero_vad.onnx",
		Size:        "2 MB",
		URL:         "https://github.com/snakers4/silero-vad/raw/master/src/silero_vad/data/silero_vad.onnx",
	}
}

// SileroVADPath returns the path to the Silero VAD ONNX model.
func SileroVADPath() string {
	return ModelPath("silero_vad.onnx")
}

// IsSileroVADDownloaded checks if the Silero VAD model is available.
func IsSileroVADDownloaded() bool {
	return IsModelDownloaded("silero_vad.onnx")
}
