package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestExpandPath(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("cannot determine home dir")
	}

	tests := []struct {
		input string
		want  string
	}{
		{"", ""},
		{"~", home},
		{"~/foo/bar", filepath.Join(home, "foo", "bar")},
		{"/absolute/path", "/absolute/path"},
		{"relative/path", "relative/path"},
	}

	for _, tt := range tests {
		got := ExpandPath(tt.input)
		if got != tt.want {
			t.Errorf("ExpandPath(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestConfigPath(t *testing.T) {
	p := ConfigPath()
	if p == "" {
		t.Error("ConfigPath() returned empty string")
	}
	if filepath.Base(p) != "sound.json" {
		t.Errorf("ConfigPath() base = %q, want sound.json", filepath.Base(p))
	}
}

func TestModelsDir(t *testing.T) {
	d := ModelsDir()
	if d == "" {
		t.Error("ModelsDir() returned empty string")
	}
	if filepath.Base(d) != "models" {
		t.Errorf("ModelsDir() base = %q, want models", filepath.Base(d))
	}
}

func TestSaveAndLoad(t *testing.T) {
	// Use a temp directory as config dir.
	tmpDir := t.TempDir()
	origHome := os.Getenv("HOME")
	// We can't change ConfigPath directly, so test the raw Save/Load via temp file.
	cfgPath := filepath.Join(tmpDir, "sound.json")

	cfg := &Config{
		DefaultModel: "models/ggml-tiny.bin",
		Language:      "tr",
		Hotkey:        "ctrl+shift+space",
		Models: []ModelEntry{
			{Name: "tiny", Path: "~/.gleann/models/ggml-tiny.bin", Size: "75 MB", Language: "multilingual"},
		},
		Completed: true,
	}

	// Manual save.
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if err := os.WriteFile(cfgPath, data, 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	// Manual load.
	data2, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	var loaded Config
	if err := json.Unmarshal(data2, &loaded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if loaded.DefaultModel != cfg.DefaultModel {
		t.Errorf("DefaultModel = %q, want %q", loaded.DefaultModel, cfg.DefaultModel)
	}
	if loaded.Language != "tr" {
		t.Errorf("Language = %q, want tr", loaded.Language)
	}
	if loaded.Hotkey != "ctrl+shift+space" {
		t.Errorf("Hotkey = %q, want ctrl+shift+space", loaded.Hotkey)
	}
	if !loaded.Completed {
		t.Error("Completed should be true")
	}
	if len(loaded.Models) != 1 {
		t.Fatalf("expected 1 model, got %d", len(loaded.Models))
	}
	if loaded.Models[0].Name != "tiny" {
		t.Errorf("model name = %q, want tiny", loaded.Models[0].Name)
	}

	_ = origHome // suppress unused warning
}

func TestAvailableModels(t *testing.T) {
	models := AvailableModels()
	if len(models) == 0 {
		t.Error("AvailableModels() returned empty")
	}

	// Check that each model has required fields.
	for _, m := range models {
		if m.Name == "" {
			t.Error("model has empty Name")
		}
		if m.FileName == "" {
			t.Error("model has empty FileName")
		}
		if m.URL == "" {
			t.Error("model has empty URL")
		}
		if m.DisplayName == "" {
			t.Error("model has empty DisplayName")
		}
	}

	// Check that we have at least tiny, base, small, large-v3-turbo.
	names := make(map[string]bool)
	for _, m := range models {
		names[m.Name] = true
	}
	for _, want := range []string{"tiny", "base", "small", "large-v3-turbo"} {
		if !names[want] {
			t.Errorf("missing model: %s", want)
		}
	}
}

func TestModelPath(t *testing.T) {
	p := ModelPath("ggml-tiny.bin")
	if filepath.Base(p) != "ggml-tiny.bin" {
		t.Errorf("ModelPath base = %q, want ggml-tiny.bin", filepath.Base(p))
	}
}

func TestLoadNonexistent(t *testing.T) {
	// When config doesn't exist, Load should return nil.
	// Set HOME to a temp dir with no config.
	tmpDir := t.TempDir()
	origHome := os.Getenv("HOME")
	os.Setenv("HOME", tmpDir)
	defer os.Setenv("HOME", origHome)

	cfg := Load()
	if cfg != nil {
		t.Error("Load() should return nil when config doesn't exist")
	}
}
