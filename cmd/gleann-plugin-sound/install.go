package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"
	"github.com/tevfik/gleann-plugin-sound/internal/config"
	"github.com/tevfik/gleann-plugin-sound/internal/httpserver"
)

// Supported audio/video extensions for document extraction.
var audioExtensions = httpserver.SupportedExts

// pluginEntry holds the fields this tool writes for its own entry. install
// edits plugins.json as raw JSON, so fields that are not listed here (token,
// ...) survive on every entry.
type pluginEntry struct {
	Name          string   `json:"name"`
	URL           string   `json:"url"`
	Command       []string `json:"command"`
	Capabilities  []string `json:"capabilities"`
	Extensions    []string `json:"extensions"`
	Timeout       int      `json:"timeout,omitempty"`
	Version       string   `json:"version,omitempty"`
	SchemaVersion int      `json:"schema_version,omitempty"`
	Priority      int      `json:"priority,omitempty"`
}

type pluginRegistry struct {
	Plugins []pluginEntry `json:"plugins"`
}

// newInstallPluginCmd creates the "install" subcommand.
//
// It registers gleann-plugin-sound as an audio-transcription plugin in ~/.gleann/plugins.json
// so that gleann's PluginManager can auto-start and route audio files to it.
//
// Usage:
//
//	gleann-plugin-sound install [--port 8767]
func newInstallPluginCmd() *cobra.Command {
	var port int

	cmd := &cobra.Command{
		Use:   "install",
		Short: "Register gleann-plugin-sound as a plugin in ~/.gleann/plugins.json",
		Long: `Registers gleann-plugin-sound in the gleann plugin registry so that
audio/video files are automatically transcribed during 'gleann build'.

This writes an entry to ~/.gleann/plugins.json with the current binary path,
model configuration, and supported file extensions. If an existing entry
for gleann-plugin-sound exists, it is replaced.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return installPlugin(cmd, port)
		},
	}

	cmd.Flags().IntVar(&port, "port", 8767, "HTTP port for the plugin server")

	return cmd
}

func installPlugin(cmd *cobra.Command, port int) error {
	// Resolve the absolute path to this binary.
	exePath, err := os.Executable()
	if err != nil {
		return fmt.Errorf("resolve executable path: %w", err)
	}
	exePath, err = filepath.EvalSymlinks(exePath)
	if err != nil {
		return fmt.Errorf("resolve symlinks: %w", err)
	}

	// Build the auto-start command.
	command := []string{exePath, "plugin-serve", "--port", fmt.Sprintf("%d", port)}

	// Inherit model/backend/language from sound.json. Every field that is set
	// counts, also before the setup wizard marked the config completed: a
	// config written by "models download" already names the downloaded model.
	if cfg := config.Load(); cfg != nil {
		model, backend := config.ExpandPath(cfg.DefaultModel), cfg.Backend
		if model == "" {
			model, backend = soleInstalledModel(cfg.Models, backend)
		}
		if model != "" {
			command = append(command, "--model", model)
		}
		if backend != "" {
			command = append(command, "--backend", backend)
		}
		if cfg.Language != "" {
			command = append(command, "--language", cfg.Language)
		}
		if cfg.RefineModel != "" {
			command = append(command, "--refine-model", config.ExpandPath(cfg.RefineModel))
		}
	}

	// Flags override the config only when given on the command line. Their
	// defaults (e.g. ~/.gleann/models/ggml-base.en.bin) must not replace a
	// model from sound.json.
	if f := cmd.Flags().Lookup("model"); f != nil && f.Changed {
		command = replaceFlag(command, "--model", config.ExpandPath(f.Value.String()))
	}
	if f := cmd.Flags().Lookup("backend"); f != nil && f.Changed {
		command = replaceFlag(command, "--backend", f.Value.String())
	}

	if model := flagValue(command, "--model"); model == "" {
		fmt.Fprintln(os.Stderr, "Warning: no model configured; plugin-serve will use its default. Set one with --model or 'gleann-plugin-sound tui'.")
	} else if _, err := os.Stat(model); err != nil {
		fmt.Fprintf(os.Stderr, "Warning: model %s not found; download it with 'gleann-plugin-sound models download'.\n", model)
	}

	// Read existing registry.
	home, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("user home dir: %w", err)
	}
	pluginsFile := filepath.Join(home, ".gleann", "plugins.json")

	// The registry is edited as raw JSON: gleann owns fields this tool does not
	// know (token, ...), on every entry and at the top level, and a typed
	// round-trip would silently drop them.
	top := map[string]json.RawMessage{}
	var rawEntries []json.RawMessage
	if data, err := os.ReadFile(pluginsFile); err == nil && len(data) > 0 {
		if err := json.Unmarshal(data, &top); err != nil {
			return fmt.Errorf("%s is not valid JSON (%v); fix or remove it and run install again: refusing to overwrite the registry", pluginsFile, err)
		} else if raw, ok := top["plugins"]; ok {
			if err := json.Unmarshal(raw, &rawEntries); err != nil {
				return fmt.Errorf("the \"plugins\" list in %s is malformed (%v); fix it and run install again: refusing to overwrite the registry", pluginsFile, err)
			}
		}
	}

	// Remove old entry if present (update).
	// Also remove legacy "gleann-sound" entries and any plugin on the same port.
	// The previous sound entry's unknown fields (e.g. token) carry over.
	pluginURL := fmt.Sprintf("http://localhost:%d", port)
	kept := make([]json.RawMessage, 0, len(rawEntries)+1)
	prev := map[string]json.RawMessage{}
	for _, raw := range rawEntries {
		var id struct{ Name, URL string }
		_ = json.Unmarshal(raw, &id)
		if id.Name == "gleann-plugin-sound" || id.Name == "gleann-sound" || id.URL == pluginURL {
			if id.Name == "gleann-plugin-sound" {
				_ = json.Unmarshal(raw, &prev)
			}
			continue
		}
		kept = append(kept, raw)
	}

	// Add new entry: our fields over whatever the previous entry held.
	entry := pluginEntry{
		Name:          "gleann-plugin-sound",
		URL:           fmt.Sprintf("http://localhost:%d", port),
		Command:       command,
		Capabilities:  httpserver.Capabilities,
		Extensions:    audioExtensions,
		Version:       httpserver.PluginVersion,
		SchemaVersion: httpserver.SchemaVersion,
		Timeout:       1800,
	}
	entryJSON, err := json.Marshal(entry)
	if err != nil {
		return fmt.Errorf("marshal plugin entry: %w", err)
	}
	var ours map[string]json.RawMessage
	_ = json.Unmarshal(entryJSON, &ours)
	for k, v := range ours {
		prev[k] = v
	}
	merged, err := json.Marshal(prev)
	if err != nil {
		return fmt.Errorf("marshal plugin entry: %w", err)
	}
	kept = append(kept, merged)
	pluginsJSON, err := json.Marshal(kept)
	if err != nil {
		return fmt.Errorf("marshal plugin list: %w", err)
	}
	top["plugins"] = pluginsJSON

	// Ensure ~/.gleann/ exists.
	if err := os.MkdirAll(filepath.Dir(pluginsFile), 0o755); err != nil {
		return fmt.Errorf("create config dir: %w", err)
	}

	// Write registry.
	data, err := json.MarshalIndent(top, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal registry: %w", err)
	}
	if err := writeFileAtomic(pluginsFile, data, 0o644); err != nil {
		return fmt.Errorf("write plugins file: %w", err)
	}

	fmt.Printf("Plugin 'gleann-plugin-sound' registered to %s\n", pluginsFile)
	fmt.Printf("  URL:          %s\n", entry.URL)
	fmt.Printf("  Command:      %v\n", entry.Command)
	fmt.Printf("  Extensions:   %v\n", entry.Extensions)
	return nil
}

// soleInstalledModel picks the model when sound.json names none: the only
// listed model that exists on disk and fits backend (any backend when it is
// empty). It returns that model and its backend, or ("", backend) when there
// is no such model or more than one.
func soleInstalledModel(models []config.ModelEntry, backend string) (string, string) {
	var pick config.ModelEntry
	n := 0
	for _, e := range models {
		b := config.BackendForModel(e)
		if b == "" || (backend != "" && b != backend) {
			continue
		}
		if _, err := os.Stat(config.ExpandPath(e.Path)); err != nil {
			continue
		}
		pick, n = e, n+1
	}
	if n != 1 {
		return "", backend
	}
	return config.ExpandPath(pick.Path), config.BackendForModel(pick)
}

// flagValue returns the value that follows flag in cmd, or "".
func flagValue(cmd []string, flag string) string {
	for i, arg := range cmd {
		if arg == flag && i+1 < len(cmd) {
			return cmd[i+1]
		}
	}
	return ""
}

// replaceFlag replaces or appends a --flag value pair in a command slice.
func replaceFlag(cmd []string, flag, value string) []string {
	for i, arg := range cmd {
		if arg == flag && i+1 < len(cmd) {
			cmd[i+1] = value
			return cmd
		}
	}
	return append(cmd, flag, value)
}

// writeFileAtomic replaces path with data: it writes a temp file in the same
// directory, gives it the existing file's mode (perm for a new file), and
// renames it over the target. A symlink at path is followed, so the link
// stays and its target is updated.
func writeFileAtomic(path string, data []byte, perm os.FileMode) error {
	if resolved, err := filepath.EvalSymlinks(path); err == nil {
		path = resolved
	}
	if st, err := os.Stat(path); err == nil {
		perm = st.Mode().Perm()
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	cleanup := func() { _ = os.Remove(tmpName) }
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		cleanup()
		return err
	}
	if err := tmp.Chmod(perm); err != nil {
		tmp.Close()
		cleanup()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		cleanup()
		return err
	}
	if err := tmp.Close(); err != nil {
		cleanup()
		return err
	}
	if err := os.Rename(tmpName, path); err != nil {
		cleanup()
		return err
	}
	return nil
}
