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

// pluginEntry mirrors the gleann Plugin struct for JSON serialisation.
// pluginEntry mirrors gleann's registry entry. Every field gleann knows must be
// listed: install rewrites the whole file, and anything missing here would be
// stripped from the other plugins' entries.
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

	// Inherit model/backend/language from config if available.
	if cfg := config.Load(); cfg != nil && cfg.Completed {
		if cfg.DefaultModel != "" {
			command = append(command, "--model", cfg.DefaultModel)
		}
		if cfg.Backend != "" {
			command = append(command, "--backend", cfg.Backend)
		}
		if cfg.Language != "" {
			command = append(command, "--language", cfg.Language)
		}
		if cfg.RefineModel != "" {
			command = append(command, "--refine-model", config.ExpandPath(cfg.RefineModel))
		}
	}

	// Also check CLI flags (they override config).
	if model, _ := cmd.Flags().GetString("model"); model != "" && model != "models/ggml-base.en.bin" {
		// Replace any existing --model in command.
		command = replaceFlag(command, "--model", model)
	}
	if backend, _ := cmd.Flags().GetString("backend"); backend != "" && backend != "whisper" {
		command = replaceFlag(command, "--backend", backend)
	}

	// Read existing registry.
	home, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("user home dir: %w", err)
	}
	pluginsFile := filepath.Join(home, ".gleann", "plugins.json")

	registry := pluginRegistry{Plugins: []pluginEntry{}}
	if data, err := os.ReadFile(pluginsFile); err == nil {
		if len(data) > 0 {
			if err := json.Unmarshal(data, &registry); err != nil {
				fmt.Fprintf(os.Stderr, "Warning: could not parse %s, creating fresh: %v\n", pluginsFile, err)
				registry = pluginRegistry{Plugins: []pluginEntry{}}
			}
		}
	}

	// Remove old entry if present (update).
	// Also remove legacy "gleann-sound" entries and any plugin on the same port.
	pluginURL := fmt.Sprintf("http://localhost:%d", port)
	filtered := make([]pluginEntry, 0, len(registry.Plugins))
	for _, p := range registry.Plugins {
		if p.Name != "gleann-plugin-sound" && p.Name != "gleann-sound" && p.URL != pluginURL {
			filtered = append(filtered, p)
		}
	}
	registry.Plugins = filtered

	// Add new entry.
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
	registry.Plugins = append(registry.Plugins, entry)

	// Ensure ~/.gleann/ exists.
	if err := os.MkdirAll(filepath.Dir(pluginsFile), 0o755); err != nil {
		return fmt.Errorf("create config dir: %w", err)
	}

	// Write registry.
	data, err := json.MarshalIndent(registry, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal registry: %w", err)
	}
	if err := os.WriteFile(pluginsFile, data, 0o644); err != nil {
		return fmt.Errorf("write plugins file: %w", err)
	}

	fmt.Printf("Plugin 'gleann-plugin-sound' registered to %s\n", pluginsFile)
	fmt.Printf("  URL:          %s\n", entry.URL)
	fmt.Printf("  Command:      %v\n", entry.Command)
	fmt.Printf("  Extensions:   %v\n", entry.Extensions)
	return nil
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
