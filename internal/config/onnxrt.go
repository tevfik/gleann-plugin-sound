package config

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

// ONNXRuntimeVersion is the pinned ONNX Runtime version to download.
const ONNXRuntimeVersion = "1.26.0"

// ONNXRuntimeInfo describes a platform-specific ONNX Runtime download.
type ONNXRuntimeInfo struct {
	URL     string // full archive download URL
	Archive string // "tgz" or "zip"
	LibName string // library filename (e.g. "libonnxruntime.so.1.21.1")
	LibGlob string // path pattern inside the archive to match
}

// GetONNXRuntimeInfo returns download metadata for the current OS and arch.
// Returns nil if the platform is unsupported.
func GetONNXRuntimeInfo() *ONNXRuntimeInfo {
	ver := ONNXRuntimeVersion
	base := "https://github.com/microsoft/onnxruntime/releases/download/v" + ver

	switch runtime.GOOS {
	case "linux":
		switch runtime.GOARCH {
		case "amd64":
			slug := "onnxruntime-linux-x64-" + ver
			return &ONNXRuntimeInfo{
				URL:     base + "/" + slug + ".tgz",
				Archive: "tgz",
				LibName: "libonnxruntime.so." + ver,
				LibGlob: slug + "/lib/libonnxruntime.so." + ver,
			}
		case "arm64":
			slug := "onnxruntime-linux-aarch64-" + ver
			return &ONNXRuntimeInfo{
				URL:     base + "/" + slug + ".tgz",
				Archive: "tgz",
				LibName: "libonnxruntime.so." + ver,
				LibGlob: slug + "/lib/libonnxruntime.so." + ver,
			}
		}
	case "darwin":
		var arch string
		switch runtime.GOARCH {
		case "amd64":
			arch = "x86_64"
		case "arm64":
			arch = "arm64"
		default:
			return nil
		}
		slug := "onnxruntime-osx-" + arch + "-" + ver
		return &ONNXRuntimeInfo{
			URL:     base + "/" + slug + ".tgz",
			Archive: "tgz",
			LibName: "libonnxruntime." + ver + ".dylib",
			LibGlob: slug + "/lib/libonnxruntime." + ver + ".dylib",
		}
	case "windows":
		if runtime.GOARCH == "amd64" {
			slug := "onnxruntime-win-x64-" + ver
			return &ONNXRuntimeInfo{
				URL:     base + "/" + slug + ".zip",
				Archive: "zip",
				LibName: "onnxruntime.dll",
				LibGlob: slug + "/lib/onnxruntime.dll",
			}
		}
	}
	return nil
}

// ONNXRuntimePath returns the expected path to the downloaded library.
// Returns "" if the platform is unsupported.
func ONNXRuntimePath() string {
	info := GetONNXRuntimeInfo()
	if info == nil {
		return ""
	}
	return filepath.Join(LibDir(), info.LibName)
}

// IsONNXRuntimeDownloaded checks if the ONNX Runtime library exists in LibDir.
func IsONNXRuntimeDownloaded() bool {
	p := ONNXRuntimePath()
	if p == "" {
		return false
	}
	_, err := os.Stat(p)
	return err == nil
}

// DownloadONNXRuntime downloads and extracts the ONNX Runtime shared library
// for the current platform into ~/.gleann/lib/. Returns the library path.
func DownloadONNXRuntime() (string, error) {
	info := GetONNXRuntimeInfo()
	if info == nil {
		return "", fmt.Errorf("unsupported platform: %s/%s", runtime.GOOS, runtime.GOARCH)
	}

	destDir := LibDir()
	destPath := filepath.Join(destDir, info.LibName)

	// Already present?
	if _, err := os.Stat(destPath); err == nil {
		return destPath, nil
	}

	if err := os.MkdirAll(destDir, 0o755); err != nil {
		return "", fmt.Errorf("create lib dir: %w", err)
	}

	log.Printf("[onnx] downloading ONNX Runtime v%s for %s/%s...", ONNXRuntimeVersion, runtime.GOOS, runtime.GOARCH)

	archivePath := filepath.Join(destDir, "onnxruntime-download.tmp")
	defer os.Remove(archivePath)

	if err := httpDownloadFile(info.URL, archivePath); err != nil {
		return "", fmt.Errorf("download failed: %w", err)
	}

	var err error
	switch info.Archive {
	case "tgz":
		err = extractFromTgz(archivePath, info.LibGlob, destPath)
	case "zip":
		err = extractFromZip(archivePath, info.LibGlob, destPath)
	default:
		err = fmt.Errorf("unknown archive type: %s", info.Archive)
	}
	if err != nil {
		os.Remove(destPath)
		return "", fmt.Errorf("extract failed: %w", err)
	}

	log.Printf("[onnx] ONNX Runtime installed: %s", destPath)
	return destPath, nil
}

// httpDownloadFile downloads url to dest using an atomic .tmp rename.
func httpDownloadFile(url, dest string) error {
	resp, err := http.Get(url)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("HTTP %d: %s", resp.StatusCode, resp.Status)
	}

	tmp := dest + ".download"
	f, err := os.Create(tmp)
	if err != nil {
		return err
	}
	defer func() {
		f.Close()
		os.Remove(tmp) // clean up on failure
	}()

	if _, err := io.Copy(f, resp.Body); err != nil {
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}

	return os.Rename(tmp, dest)
}

// PipPackageForGPU returns the pip package name that provides GPU-accelerated
// ONNX Runtime for the current platform. Returns "" if no separate GPU package
// exists (e.g. macOS where CoreML is included in the base package).
func PipPackageForGPU() string {
	switch runtime.GOOS {
	case "linux", "windows":
		return "onnxruntime-gpu"
	}
	return "" // macOS: base onnxruntime includes CoreML
}

// PipInstallDir returns the target directory for pip-installed ONNX Runtime.
func PipInstallDir() string {
	return filepath.Join(LibDir(), "onnxrt-pip")
}

// FindPipInstalledONNXRuntime searches the pip install directory for the ONNX
// Runtime shared library. Returns the path if found, "" otherwise.
func FindPipInstalledONNXRuntime() string {
	pipDir := PipInstallDir()
	capiDir := filepath.Join(pipDir, "onnxruntime", "capi")

	var libGlob string
	switch runtime.GOOS {
	case "linux":
		libGlob = "libonnxruntime.so.*"
	case "darwin":
		libGlob = "libonnxruntime.*.dylib"
	case "windows":
		libGlob = "onnxruntime.dll"
	default:
		return ""
	}

	matches, _ := filepath.Glob(filepath.Join(capiDir, libGlob))
	if len(matches) > 0 {
		return matches[0]
	}
	return ""
}

// HasPipInstalledCUDAProvider checks if the pip-installed ONNX Runtime includes
// the CUDA execution provider.
func HasPipInstalledCUDAProvider() bool {
	capiDir := filepath.Join(PipInstallDir(), "onnxruntime", "capi")
	_, err := os.Stat(filepath.Join(capiDir, "libonnxruntime_providers_cuda.so"))
	return err == nil
}

// FindPip returns the path to pip3 or pip, whichever is available.
// Returns "" if neither is found.
func FindPip() string {
	for _, name := range []string{"pip3", "pip"} {
		if p, err := exec.LookPath(name); err == nil {
			return p
		}
	}
	return ""
}

// InstallONNXRuntimeGPUViaPip installs the GPU-accelerated ONNX Runtime via pip
// into ~/.gleann/lib/onnxrt-pip/. Returns the library path on success.
func InstallONNXRuntimeGPUViaPip() (string, error) {
	pkg := PipPackageForGPU()
	if pkg == "" {
		return "", fmt.Errorf("no GPU pip package for %s (CoreML is included in base onnxruntime)", runtime.GOOS)
	}

	pipBin := FindPip()
	if pipBin == "" {
		return "", fmt.Errorf("pip not found: install Python 3 and pip, then retry")
	}

	targetDir := PipInstallDir()
	if err := os.MkdirAll(targetDir, 0o755); err != nil {
		return "", fmt.Errorf("create pip target dir: %w", err)
	}

	log.Printf("[onnx] installing %s via pip to %s...", pkg, targetDir)

	cmd := exec.Command(pipBin, "install", "--target", targetDir, "--upgrade", pkg)
	cmd.Stdout = os.Stderr // show pip output in logs
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("pip install %s failed: %w", pkg, err)
	}

	libPath := FindPipInstalledONNXRuntime()
	if libPath == "" {
		return "", fmt.Errorf("pip install succeeded but library not found in %s", targetDir)
	}

	log.Printf("[onnx] GPU ONNX Runtime installed: %s", libPath)
	return libPath, nil
}

// extractFromTgz extracts a single file matching targetGlob from a .tar.gz archive.
func extractFromTgz(archivePath, targetGlob, destPath string) error {
	f, err := os.Open(archivePath)
	if err != nil {
		return err
	}
	defer f.Close()

	gz, err := gzip.NewReader(f)
	if err != nil {
		return err
	}
	defer gz.Close()

	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}

		// Match using filepath.Match or exact string match.
		name := strings.TrimPrefix(hdr.Name, "./")
		matched, _ := filepath.Match(targetGlob, name)
		if !matched && name != targetGlob {
			continue
		}

		// Found the target — extract it.
		tmp := destPath + ".tmp"
		out, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o755)
		if err != nil {
			return err
		}
		if _, err := io.Copy(out, tr); err != nil {
			out.Close()
			os.Remove(tmp)
			return err
		}
		out.Close()
		return os.Rename(tmp, destPath)
	}

	return fmt.Errorf("file %q not found in archive", targetGlob)
}

// extractFromZip extracts a single file matching targetGlob from a .zip archive.
func extractFromZip(archivePath, targetGlob, destPath string) error {
	zr, err := zip.OpenReader(archivePath)
	if err != nil {
		return err
	}
	defer zr.Close()

	for _, zf := range zr.File {
		// Zip uses forward slashes regardless of OS.
		name := zf.Name
		matched, _ := filepath.Match(targetGlob, name)
		if !matched && name != targetGlob {
			continue
		}

		rc, err := zf.Open()
		if err != nil {
			return err
		}
		defer rc.Close()

		tmp := destPath + ".tmp"
		out, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o755)
		if err != nil {
			return err
		}
		if _, err := io.Copy(out, rc); err != nil {
			out.Close()
			os.Remove(tmp)
			return err
		}
		out.Close()
		return os.Rename(tmp, destPath)
	}

	return fmt.Errorf("file %q not found in archive", targetGlob)
}
