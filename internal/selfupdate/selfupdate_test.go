package selfupdate

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const bin = "gleann-plugin-sound"

func TestIsNewer(t *testing.T) {
	for _, c := range []struct {
		cur, latest string
		newer, ok   bool
	}{
		{"0.1.5", "v0.2.0", true, true},
		{"v0.1.5-dirty", "v0.1.5", false, true},
		{"1.10.0", "v1.9.0", false, true},
		{"dev", "v1.4.0", false, false},
	} {
		n, ok := IsNewer(c.cur, c.latest)
		if n != c.newer || ok != c.ok {
			t.Errorf("IsNewer(%q,%q)=%v,%v", c.cur, c.latest, n, ok)
		}
	}
}

func TestAssetName(t *testing.T) {
	for flavor, infix := range map[string]string{
		"whisper": "", "stub": "-stub", "onnx": "-onnx", "whisper-onnx": "-whisper-onnx",
	} {
		got := AssetName(bin, flavor, "v1.0.0")
		if !strings.HasPrefix(got, bin+infix+"-v1.0.0-") || !strings.HasSuffix(got, ".tar.gz") {
			t.Errorf("AssetName(%q) = %q", flavor, got)
		}
	}
}

// release serves a fake GitHub release carrying one asset for flavor.
func release(t *testing.T, flavor, content, sumOverride string) {
	t.Helper()
	asset := AssetName(bin, flavor, "v1.4.0")
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	tw.WriteHeader(&tar.Header{Name: strings.TrimSuffix(asset, ".tar.gz"), Mode: 0o755, Size: int64(len(content)), Typeflag: tar.TypeReg})
	tw.Write([]byte(content))
	tw.Close()
	gz.Close()
	sum := sha256.Sum256(buf.Bytes())
	hash := hex.EncodeToString(sum[:])
	if sumOverride != "" {
		hash = sumOverride
	}
	mux := http.NewServeMux()
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	mux.HandleFunc("/repos/tevfik/"+bin+"/releases/latest", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, `{"tag_name":"v1.4.0","assets":[{"name":%q,"browser_download_url":%q},{"name":"checksums.txt","browser_download_url":%q}]}`,
			asset, srv.URL+"/a", srv.URL+"/c")
	})
	mux.HandleFunc("/a", func(w http.ResponseWriter, r *http.Request) { w.Write(buf.Bytes()) })
	mux.HandleFunc("/c", func(w http.ResponseWriter, r *http.Request) { fmt.Fprintf(w, "%s  %s\n", hash, asset) })
	t.Setenv("GLEANN_GITHUB_API", srv.URL)
}

func TestInstallReplacesBinaryAfterChecksum(t *testing.T) {
	release(t, "whisper-onnx", "NEW", "")
	dest := filepath.Join(t.TempDir(), bin)
	os.WriteFile(dest, []byte("OLD"), 0o755)
	rel, err := Latest(context.Background(), "tevfik", bin)
	if err != nil || rel.Tag != "v1.4.0" {
		t.Fatalf("latest: %v %v", rel, err)
	}
	ok, err := Install(context.Background(), rel, bin, "whisper-onnx", dest)
	if err != nil || !ok {
		t.Fatalf("install: verified=%v err=%v", ok, err)
	}
	if b, _ := os.ReadFile(dest); string(b) != "NEW" {
		t.Fatalf("dest = %q", b)
	}
}

func TestInstallNeverSwapsFlavour(t *testing.T) {
	release(t, "onnx", "NEW", "") // only a plain onnx build is published
	dest := filepath.Join(t.TempDir(), bin)
	os.WriteFile(dest, []byte("OLD"), 0o755)
	rel, _ := Latest(context.Background(), "tevfik", bin)
	if _, err := Install(context.Background(), rel, bin, "whisper-onnx", dest); err == nil || !strings.Contains(err.Error(), "whisper-onnx") {
		t.Fatalf("err = %v", err)
	}
	if b, _ := os.ReadFile(dest); string(b) != "OLD" {
		t.Fatalf("dest = %q", b)
	}
}

func TestInstallKeepsOldBinaryOnChecksumMismatch(t *testing.T) {
	release(t, "whisper", "NEW", strings.Repeat("0", 64))
	dir := t.TempDir()
	dest := filepath.Join(dir, bin)
	os.WriteFile(dest, []byte("OLD"), 0o755)
	rel, _ := Latest(context.Background(), "tevfik", bin)
	if _, err := Install(context.Background(), rel, bin, "whisper", dest); err == nil || !strings.Contains(err.Error(), "checksum mismatch") {
		t.Fatalf("err = %v", err)
	}
	if b, _ := os.ReadFile(dest); string(b) != "OLD" {
		t.Fatalf("dest = %q", b)
	}
	if es, _ := os.ReadDir(dir); len(es) != 1 {
		t.Errorf("leftover files: %v", es)
	}
}
