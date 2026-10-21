//go:build onnx
// +build onnx

// Package ortrt finds (or downloads) the ONNX Runtime shared library and
// initialises the process-wide ONNX Runtime environment, once, for every user in
// the binary: the ASR engine, the speaker embedder and the Silero VAD.
package ortrt

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"

	ort "github.com/yalue/onnxruntime_go"

	"github.com/tevfik/gleann-plugin-sound/internal/config"
)

// discoverONNXLibrary locates the ONNX Runtime library
func discoverONNXLibrary() bool {
	for _, envKey := range []string{"ORT_SHARED_LIBRARY_PATH", "ORT_LIB_PATH"} {
		if p := os.Getenv(envKey); p != "" {
			log.Printf("[ortrt] using library from %s=%s", envKey, p)
			ort.SetSharedLibraryPath(p)
			return true
		}
	}

	if libPath := config.ONNXRuntimePath(); libPath != "" {
		if _, err := os.Stat(libPath); err == nil {
			log.Printf("[ortrt] using cached library: %s", libPath)
			ort.SetSharedLibraryPath(libPath)
			return true
		}
	}

	if libPath := config.FindPipInstalledONNXRuntime(); libPath != "" {
		ort.SetSharedLibraryPath(libPath)
		return true
	}

	searchGlobs := []string{
		"/usr/lib/libonnxruntime.so",
		"/usr/lib/libonnxruntime.so.*",
		"/usr/local/lib/libonnxruntime.so",
		"/usr/local/lib/libonnxruntime.so.*",
		"/usr/lib/x86_64-linux-gnu/libonnxruntime.so",
		"/usr/lib/x86_64-linux-gnu/libonnxruntime.so.*",
		"/usr/lib/aarch64-linux-gnu/libonnxruntime.so.*",
		"/opt/homebrew/lib/libonnxruntime.dylib",
	}

	if home, err := os.UserHomeDir(); err == nil {
		pyGlobs := []string{
			home + "/.local/lib/python*/site-packages/onnxruntime/capi/libonnxruntime.so.*",
		}
		searchGlobs = append(searchGlobs, pyGlobs...)
	}

	for _, pattern := range searchGlobs {
		matches, _ := filepath.Glob(pattern)
		for _, path := range matches {
			if _, err := os.Stat(path); err == nil {
				log.Printf("[ortrt] found ONNX Runtime library: %s", path)
				ort.SetSharedLibraryPath(path)
				return true
			}
		}
	}

	return false
}

var (
	mu   sync.Mutex
	done bool
)

// Ensure locates (or downloads) the library and initialises the environment. It
// is idempotent and safe for concurrent use.
func Ensure() error {
	mu.Lock()
	defer mu.Unlock()
	if done {
		return nil
	}
	if !discoverONNXLibrary() {
		log.Printf("[ortrt] runtime library not found, downloading...")
		libPath, err := config.DownloadONNXRuntime()
		if err != nil {
			return fmt.Errorf("onnx runtime not found and download failed: %w", err)
		}
		ort.SetSharedLibraryPath(libPath)
	}
	if err := ort.InitializeEnvironment(); err != nil && !strings.Contains(err.Error(), "already been initialized") {
		return fmt.Errorf("failed to initialize runtime env: %w", err)
	}
	done = true
	return nil
}
