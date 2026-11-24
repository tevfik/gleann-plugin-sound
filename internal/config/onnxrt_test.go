package config

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type fakeEntry struct{ name, body string }

func fakeNupkg(t *testing.T, entries []fakeEntry) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for _, e := range entries {
		w, err := zw.Create(e.name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write([]byte(e.body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func sumHex(b []byte) string {
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

func serve(t *testing.T, body []byte) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(body)
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}

func testInfo(url, sha string) *ONNXRuntimeInfo {
	return &ONNXRuntimeInfo{
		URL:     url,
		SHA256:  sha,
		LibName: "libonnxruntime.so.9.9.9",
		Files: []ONNXRuntimeFile{
			{Entry: "runtimes/linux-x64/native/libonnxruntime.so", Dest: "libonnxruntime.so.9.9.9"},
			{Entry: "runtimes/linux-x64/native/libonnxruntime_providers_shared.so", Dest: "libonnxruntime_providers_shared.so"},
		},
	}
}

func goodEntries() []fakeEntry {
	return []fakeEntry{
		{"[Content_Types].xml", "x"},
		{"build/native/include/onnxruntime_c_api.h", "hdr"},
		{"runtimes/linux-x64/native/libonnxruntime.so", "LIB"},
		{"runtimes/linux-x64/native/libonnxruntime_providers_shared.so", "PROV"},
		{"runtimes/linux-arm64/native/libonnxruntime.so", "ARM"},
	}
}

func TestInstallONNXRuntime_ExtractsExpectedFiles(t *testing.T) {
	pkg := fakeNupkg(t, goodEntries())
	dir := t.TempDir()
	path, err := installONNXRuntime(testInfo(serve(t, pkg), sumHex(pkg)), dir)
	if err != nil {
		t.Fatal(err)
	}
	if path != filepath.Join(dir, "libonnxruntime.so.9.9.9") {
		t.Fatalf("path = %s", path)
	}
	for name, want := range map[string]string{
		"libonnxruntime.so.9.9.9":            "LIB",
		"libonnxruntime_providers_shared.so": "PROV",
	} {
		got, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil || string(got) != want {
			t.Fatalf("%s = %q, %v", name, got, err)
		}
	}
	ents, _ := os.ReadDir(dir)
	if len(ents) != 2 {
		t.Fatalf("unexpected files left in dir: %v", ents)
	}
}

func TestInstallONNXRuntime_ShaMismatch(t *testing.T) {
	pkg := fakeNupkg(t, goodEntries())
	dir := t.TempDir()
	_, err := installONNXRuntime(testInfo(serve(t, pkg), strings.Repeat("0", 64)), dir)
	if err == nil || !strings.Contains(err.Error(), "sha256") {
		t.Fatalf("want sha256 error, got %v", err)
	}
	if ents, _ := os.ReadDir(dir); len(ents) != 0 {
		t.Fatalf("files left after mismatch: %v", ents)
	}
}

func TestInstallONNXRuntime_RejectsZipSlip(t *testing.T) {
	for _, bad := range []string{"../evil.so", "runtimes/../../evil", "/abs/evil", `a\..\evil`} {
		entries := append(goodEntries(), fakeEntry{bad, "evil"})
		pkg := fakeNupkg(t, entries)
		dir := t.TempDir()
		_, err := installONNXRuntime(testInfo(serve(t, pkg), sumHex(pkg)), dir)
		if err == nil || !strings.Contains(err.Error(), "unsafe") {
			t.Fatalf("%q: want unsafe-path error, got %v", bad, err)
		}
		if ents, _ := os.ReadDir(dir); len(ents) != 0 {
			t.Fatalf("%q: files written: %v", bad, ents)
		}
	}
}

func TestInstallONNXRuntime_MissingEntry(t *testing.T) {
	pkg := fakeNupkg(t, []fakeEntry{{"build/x", "y"}})
	_, err := installONNXRuntime(testInfo(serve(t, pkg), sumHex(pkg)), t.TempDir())
	if err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("want not-found error, got %v", err)
	}
}

func TestInstallONNXRuntime_EntryTooLarge(t *testing.T) {
	pkg := fakeNupkg(t, goodEntries())
	info := testInfo(serve(t, pkg), sumHex(pkg))
	info.maxEntryBytes = 2 // "LIB" is 3 bytes
	dir := t.TempDir()
	if _, err := installONNXRuntime(info, dir); err == nil || !strings.Contains(err.Error(), "too large") {
		t.Fatalf("want too-large error, got %v", err)
	}
	if ents, _ := os.ReadDir(dir); len(ents) != 0 {
		t.Fatalf("files left: %v", ents)
	}
}

func TestInstallONNXRuntime_HTTPError(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	defer srv.Close()
	if _, err := installONNXRuntime(testInfo(srv.URL, strings.Repeat("0", 64)), t.TempDir()); err == nil {
		t.Fatal("want error on 404")
	}
}

func TestONNXRuntimeInfoFor(t *testing.T) {
	for _, c := range []struct{ goos, goarch, entry, lib string }{
		{"linux", "amd64", "runtimes/linux-x64/native/libonnxruntime.so", "libonnxruntime.so." + ONNXRuntimeVersion},
		{"linux", "arm64", "runtimes/linux-arm64/native/libonnxruntime.so", "libonnxruntime.so." + ONNXRuntimeVersion},
		{"darwin", "arm64", "runtimes/osx-arm64/native/libonnxruntime.dylib", "libonnxruntime." + ONNXRuntimeVersion + ".dylib"},
		{"windows", "amd64", "runtimes/win-x64/native/onnxruntime.dll", "onnxruntime.dll"},
		{"windows", "arm64", "runtimes/win-arm64/native/onnxruntime.dll", "onnxruntime.dll"},
	} {
		info := onnxRuntimeInfoFor(c.goos, c.goarch)
		if info == nil {
			t.Fatalf("%s/%s unsupported", c.goos, c.goarch)
		}
		if !strings.HasPrefix(info.URL, "https://api.nuget.org/") || strings.Contains(info.URL, "github.com") {
			t.Fatalf("%s/%s URL = %s", c.goos, c.goarch, info.URL)
		}
		if len(info.SHA256) != 64 || info.LibName != c.lib || info.Files[0].Entry != c.entry || info.Files[0].Dest != c.lib {
			t.Fatalf("%s/%s info = %+v", c.goos, c.goarch, info)
		}
	}
}

func TestONNXRuntimeInfoFor_Unsupported(t *testing.T) {
	for _, p := range [][2]string{{"darwin", "amd64"}, {"freebsd", "amd64"}, {"linux", "riscv64"}} {
		if onnxRuntimeInfoFor(p[0], p[1]) != nil {
			t.Fatalf("%v should be unsupported", p)
		}
	}
	if _, err := installONNXRuntime(nil, t.TempDir()); err == nil {
		t.Fatal("nil info must error")
	}
}
