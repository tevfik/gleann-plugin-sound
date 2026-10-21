package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
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
