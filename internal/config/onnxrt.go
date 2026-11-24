package config

import (
	"archive/zip"
	"crypto/sha256"
	"encoding/hex"
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

// onnxRuntimeNupkgSHA256 is the sha256 of microsoft.ml.onnxruntime.<version>.nupkg
// for ONNXRuntimeVersion. The official NuGet package is the download source
// (the GitHub releases of microsoft/onnxruntime are gone); it carries no
// osx-x64 build, so that platform is unsupported.
const onnxRuntimeNupkgSHA256 = "50cc3772668f04b8373ad65a36793f94699bc4e818f6e691fc68f1578c38ce42"

const (
	onnxMaxEntryBytes = 256 << 20 // per extracted entry
	onnxMaxTotalBytes = 512 << 20 // all extracted entries
	onnxMaxPkgBytes   = 512 << 20 // the downloaded package
)

// ONNXRuntimeFile maps one entry of the package to a file name in LibDir.
type ONNXRuntimeFile struct {
	Entry string // exact path inside the nupkg
	Dest  string // file name in the destination directory
}

// ONNXRuntimeInfo describes a platform-specific ONNX Runtime download.
type ONNXRuntimeInfo struct {
	URL     string            // nupkg download URL
	SHA256  string            // expected hex sha256 of the nupkg
	LibName string            // library filename the loader expects (e.g. "libonnxruntime.so.1.26.0")
	Files   []ONNXRuntimeFile // entries to extract; the first is the main library

	maxEntryBytes int64 // test override; 0 = default
}

// GetONNXRuntimeInfo returns download metadata for the current OS and arch.
// Returns nil if the platform is unsupported.
func GetONNXRuntimeInfo() *ONNXRuntimeInfo {
	return onnxRuntimeInfoFor(runtime.GOOS, runtime.GOARCH)
}

func onnxRuntimeInfoFor(goos, goarch string) *ONNXRuntimeInfo {
	ver := ONNXRuntimeVersion
	url := "https://api.nuget.org/v3-flatcontainer/microsoft.ml.onnxruntime/" + ver +
		"/microsoft.ml.onnxruntime." + ver + ".nupkg"

	var rid, entryLib, destLib string
	var extra []string // companion libraries, kept under their own names
	switch goos {
	case "linux":
		switch goarch {
		case "amd64":
			rid = "linux-x64"
		case "arm64":
			rid = "linux-arm64"
		default:
			return nil
		}
		entryLib, destLib = "libonnxruntime.so", "libonnxruntime.so."+ver
		extra = []string{"libonnxruntime_providers_shared.so"}
	case "darwin":
		if goarch != "arm64" {
			return nil
		}
		rid = "osx-arm64"
		entryLib, destLib = "libonnxruntime.dylib", "libonnxruntime."+ver+".dylib"
	case "windows":
		switch goarch {
		case "amd64":
			rid = "win-x64"
		case "arm64":
			rid = "win-arm64"
		default:
			return nil
		}
		entryLib, destLib = "onnxruntime.dll", "onnxruntime.dll"
		extra = []string{"onnxruntime_providers_shared.dll"}
	default:
		return nil
	}

	dir := "runtimes/" + rid + "/native/"
	files := []ONNXRuntimeFile{{Entry: dir + entryLib, Dest: destLib}}
	for _, x := range extra {
		files = append(files, ONNXRuntimeFile{Entry: dir + x, Dest: x})
	}
	return &ONNXRuntimeInfo{URL: url, SHA256: onnxRuntimeNupkgSHA256, LibName: destLib, Files: files}
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

// DownloadONNXRuntime downloads the ONNX Runtime shared library for the
// current platform from the official NuGet package into ~/.gleann/lib/.
// Returns the library path.
func DownloadONNXRuntime() (string, error) {
	info := GetONNXRuntimeInfo()
	if info == nil {
		return "", fmt.Errorf("no official ONNX Runtime %s package for %s/%s: install it manually and set ORT_LIB_PATH",
			ONNXRuntimeVersion, runtime.GOOS, runtime.GOARCH)
	}
	destPath := filepath.Join(LibDir(), info.LibName)
	if _, err := os.Stat(destPath); err == nil {
		return destPath, nil
	}
	log.Printf("[onnx] downloading ONNX Runtime v%s for %s/%s...", ONNXRuntimeVersion, runtime.GOOS, runtime.GOARCH)
	path, err := installONNXRuntime(info, LibDir())
	if err != nil {
		return "", err
	}
	log.Printf("[onnx] ONNX Runtime installed: %s", path)
	return path, nil
}

// installONNXRuntime downloads info.URL, verifies its sha256 while streaming,
// and only then extracts info.Files into destDir.
func installONNXRuntime(info *ONNXRuntimeInfo, destDir string) (string, error) {
	if info == nil {
		return "", fmt.Errorf("unsupported platform: %s/%s", runtime.GOOS, runtime.GOARCH)
	}
	if err := os.MkdirAll(destDir, 0o755); err != nil {
		return "", fmt.Errorf("create lib dir: %w", err)
	}
	pkg, err := os.CreateTemp(destDir, "onnxruntime-*.nupkg.tmp")
	if err != nil {
		return "", fmt.Errorf("create temp file: %w", err)
	}
	defer os.Remove(pkg.Name())
	defer pkg.Close()

	if err := downloadVerified(info.URL, info.SHA256, pkg); err != nil {
		return "", err
	}
	if err := extractNupkg(pkg.Name(), info, destDir); err != nil {
		return "", fmt.Errorf("extract ONNX Runtime: %w", err)
	}
	return filepath.Join(destDir, info.LibName), nil
}

// downloadVerified streams url into w, hashing on the way, and fails unless
// the sha256 matches want.
func downloadVerified(url, want string, w io.Writer) error {
	resp, err := http.Get(url)
	if err != nil {
		return fmt.Errorf("download ONNX Runtime: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("download ONNX Runtime: HTTP %s", resp.Status)
	}
	h := sha256.New()
	n, err := io.Copy(io.MultiWriter(w, h), io.LimitReader(resp.Body, onnxMaxPkgBytes+1))
	if err != nil {
		return fmt.Errorf("download ONNX Runtime: %w", err)
	}
	if n > onnxMaxPkgBytes {
		return fmt.Errorf("download ONNX Runtime: package larger than %d bytes", int64(onnxMaxPkgBytes))
	}
	if got := hex.EncodeToString(h.Sum(nil)); !strings.EqualFold(got, want) {
		return fmt.Errorf("ONNX Runtime package sha256 mismatch: got %s, want %s", got, want)
	}
	return nil
}

// extractNupkg extracts exactly info.Files from the (already verified) zip.
// Every entry name in the archive must be a safe relative path; each file is
// written to a temp file and renamed into place, with size caps.
func extractNupkg(path string, info *ONNXRuntimeInfo, destDir string) error {
	zr, err := zip.OpenReader(path)
	if err != nil {
		return err
	}
	defer zr.Close()

	byName := make(map[string]*zip.File, len(zr.File))
	for _, zf := range zr.File {
		n := zf.Name
		if n == "" || strings.HasPrefix(n, "/") || strings.Contains(n, `\`) {
			return fmt.Errorf("unsafe path in archive: %q", n)
		}
		for _, el := range strings.Split(n, "/") {
			if el == ".." {
				return fmt.Errorf("unsafe path in archive: %q", n)
			}
		}
		byName[n] = zf
	}

	maxEntry := int64(onnxMaxEntryBytes)
	if info.maxEntryBytes > 0 {
		maxEntry = info.maxEntryBytes
	}
	var total int64
	var written []string
	cleanup := func() {
		for _, f := range written {
			os.Remove(f)
		}
	}
	for _, f := range info.Files {
		zf, ok := byName[f.Entry]
		if !ok {
			cleanup()
			return fmt.Errorf("file %q not found in archive", f.Entry)
		}
		if f.Dest != filepath.Base(f.Dest) {
			cleanup()
			return fmt.Errorf("unsafe destination name %q", f.Dest)
		}
		if zf.UncompressedSize64 > uint64(maxEntry) {
			cleanup()
			return fmt.Errorf("entry %q too large (%d bytes)", f.Entry, zf.UncompressedSize64)
		}
		n, err := writeEntry(zf, filepath.Join(destDir, f.Dest), maxEntry, onnxMaxTotalBytes-total)
		if err != nil {
			cleanup()
			return fmt.Errorf("entry %q: %w", f.Entry, err)
		}
		total += n
		written = append(written, filepath.Join(destDir, f.Dest))
	}
	return nil
}

func writeEntry(zf *zip.File, dest string, maxEntry, remaining int64) (int64, error) {
	rc, err := zf.Open()
	if err != nil {
		return 0, err
	}
	defer rc.Close()
	tmp, err := os.CreateTemp(filepath.Dir(dest), ".onnx-*.tmp")
	if err != nil {
		return 0, err
	}
	defer os.Remove(tmp.Name())
	limit := min(maxEntry, remaining)
	n, err := io.Copy(tmp, io.LimitReader(rc, limit+1))
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return 0, err
	}
	if n > limit {
		return 0, fmt.Errorf("too large (over %d bytes)", limit)
	}
	if err := os.Chmod(tmp.Name(), 0o755); err != nil {
		return 0, err
	}
	return n, os.Rename(tmp.Name(), dest)
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
