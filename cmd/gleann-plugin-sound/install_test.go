package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/tevfik/gleann-plugin-sound/internal/config"
)

// install rewrites the shared registry; the other plugins' entries must come
// back untouched, including fields this tool does not use itself.
func TestInstallPreservesOtherPlugins(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	dir := filepath.Join(home, ".gleann")
	_ = os.MkdirAll(dir, 0o755)
	existing := `{"plugins":[
	 {"name":"gleann-plugin-docs","url":"http://localhost:8765","command":["docs"],
	  "capabilities":["document-extraction"],"extensions":[".pdf"],
	  "timeout":120,"version":"1.4.0","schema_version":2,"priority":10},
	 {"name":"gleann-plugin-sound","url":"http://localhost:8766","command":["old"],
	  "capabilities":["document-extraction"],"extensions":[".mp3"]}]}`
	_ = os.WriteFile(filepath.Join(dir, "plugins.json"), []byte(existing), 0o644)

	if err := installPlugin(newInstallPluginCmd(), 8767); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(filepath.Join(dir, "plugins.json"))
	var reg pluginRegistry
	if err := json.Unmarshal(data, &reg); err != nil {
		t.Fatal(err)
	}
	byName := map[string]pluginEntry{}
	for _, p := range reg.Plugins {
		byName[p.Name] = p
	}
	docs := byName["gleann-plugin-docs"]
	if docs.Timeout != 120 || docs.Priority != 10 || docs.SchemaVersion != 2 || docs.Version != "1.4.0" {
		t.Errorf("docs plugin entry was damaged: %+v", docs)
	}
	snd := byName["gleann-plugin-sound"]
	if snd.URL != "http://localhost:8767" {
		t.Errorf("sound entry not updated: %+v", snd)
	}
	hasAudio := false
	for _, c := range snd.Capabilities {
		hasAudio = hasAudio || c == "audio-transcription"
	}
	if !hasAudio {
		t.Errorf("sound must register audio-transcription: %v", snd.Capabilities)
	}
	if len(reg.Plugins) != 2 {
		t.Errorf("want 2 plugins, got %d", len(reg.Plugins))
	}
}

// installCmdWithRootFlags returns the install command with the persistent
// --model/--backend flags the root command gives it, using the same defaults.
func installCmdWithRootFlags() *cobra.Command {
	cmd := newInstallPluginCmd()
	cmd.Flags().String("model", config.ModelPath("ggml-base.en.bin"), "")
	cmd.Flags().String("backend", "whisper", "")
	return cmd
}

func registeredSoundCommand(t *testing.T, home string) []string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(home, ".gleann", "plugins.json"))
	if err != nil {
		t.Fatal(err)
	}
	var reg pluginRegistry
	if err := json.Unmarshal(data, &reg); err != nil {
		t.Fatal(err)
	}
	for _, p := range reg.Plugins {
		if p.Name == "gleann-plugin-sound" {
			return p.Command
		}
	}
	t.Fatal("sound plugin not registered")
	return nil
}

func writeSoundConfig(t *testing.T, home, body string) {
	t.Helper()
	dir := filepath.Join(home, ".gleann")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "sound.json"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// A sound.json written by "models download" (completed=false) names the
// downloaded model; install must register it, not the unset --model default.
func TestInstallUsesConfigModelWhenFlagNotGiven(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	model := filepath.Join(home, "nemotron.gguf")
	_ = os.WriteFile(model, []byte("x"), 0o644)
	cfg, _ := json.Marshal(map[string]any{"default_model": model, "backend": "tc", "completed": false})
	writeSoundConfig(t, home, string(cfg))

	if err := installPlugin(installCmdWithRootFlags(), 8767); err != nil {
		t.Fatal(err)
	}
	got := registeredSoundCommand(t, home)
	if v := flagValue(got, "--model"); v != model {
		t.Errorf("--model = %q, want %q (command %v)", v, model, got)
	}
	if v := flagValue(got, "--backend"); v != "tc" {
		t.Errorf("--backend = %q, want tc (command %v)", v, got)
	}
	if strings.Contains(strings.Join(got, " "), "ggml-base.en.bin") {
		t.Errorf("flag default leaked into the command: %v", got)
	}
}

// An explicit --model/--backend still overrides sound.json.
func TestInstallExplicitFlagsOverrideConfig(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	cfg, _ := json.Marshal(map[string]any{"default_model": filepath.Join(home, "a.gguf"), "backend": "tc", "completed": true})
	writeSoundConfig(t, home, string(cfg))

	cmd := installCmdWithRootFlags()
	want := filepath.Join(home, "b.bin")
	if err := cmd.Flags().Set("model", want); err != nil {
		t.Fatal(err)
	}
	if err := cmd.Flags().Set("backend", "whisper"); err != nil {
		t.Fatal(err)
	}
	if err := installPlugin(cmd, 8767); err != nil {
		t.Fatal(err)
	}
	got := registeredSoundCommand(t, home)
	if v := flagValue(got, "--model"); v != want {
		t.Errorf("--model = %q, want %q (command %v)", v, want, got)
	}
	if v := flagValue(got, "--backend"); v != "whisper" {
		t.Errorf("--backend = %q, want whisper (command %v)", v, got)
	}
	if n := strings.Count(strings.Join(got, " "), "--model"); n != 1 {
		t.Errorf("want one --model, got %d: %v", n, got)
	}
}

// "models download" lists the model but leaves default_model empty (the state
// found on a fresh machine). With exactly one usable model on disk, install
// registers it with its backend; with two it picks none.
func TestInstallPicksSoleDownloadedModel(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	gguf := filepath.Join(home, "nemotron-3.5-asr-streaming-0.6b-Q8_0.gguf")
	_ = os.WriteFile(gguf, []byte("x"), 0o644)
	models := []map[string]string{
		{"name": "nemotron-3.5-asr-q8-gguf", "path": gguf},
		{"name": "small-q5_1", "path": filepath.Join(home, "missing.bin")}, // not on disk
	}
	cfg, _ := json.Marshal(map[string]any{"default_model": "", "models": models, "completed": false})
	writeSoundConfig(t, home, string(cfg))

	if err := installPlugin(installCmdWithRootFlags(), 8767); err != nil {
		t.Fatal(err)
	}
	got := registeredSoundCommand(t, home)
	if flagValue(got, "--model") != gguf || flagValue(got, "--backend") != "tc" {
		t.Fatalf("want --model %s --backend tc, got %v", gguf, got)
	}

	// A second usable model makes the choice ambiguous.
	bin := filepath.Join(home, "ggml-small-q5_1.bin")
	_ = os.WriteFile(bin, []byte("x"), 0o644)
	models[1]["path"] = bin
	cfg, _ = json.Marshal(map[string]any{"default_model": "", "models": models})
	writeSoundConfig(t, home, string(cfg))
	if err := installPlugin(installCmdWithRootFlags(), 8767); err != nil {
		t.Fatal(err)
	}
	if got := registeredSoundCommand(t, home); flagValue(got, "--model") != "" {
		t.Fatalf("ambiguous models must not be picked, got %v", got)
	}
}
