// Package selfupdate replaces the running plugin binary with the latest
// GitHub release of the same repository.
package selfupdate

import (
	"archive/tar"
	"bufio"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"time"
)

// APIBase is the GitHub REST root. GLEANN_GITHUB_API overrides it (mirror,
// GitHub Enterprise, tests).
func APIBase() string {
	if v := os.Getenv("GLEANN_GITHUB_API"); v != "" {
		return strings.TrimRight(v, "/")
	}
	return "https://api.github.com"
}

type Asset struct {
	Name string `json:"name"`
	URL  string `json:"browser_download_url"`
}

type Release struct {
	Tag    string  `json:"tag_name"`
	Assets []Asset `json:"assets"`
}

var client = &http.Client{Timeout: 10 * time.Minute}

func get(ctx context.Context, url string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		return nil, fmt.Errorf("GET %s: HTTP %d", url, resp.StatusCode)
	}
	return resp, nil
}

// Latest returns the latest published release of owner/repo.
func Latest(ctx context.Context, owner, repo string) (*Release, error) {
	resp, err := get(ctx, fmt.Sprintf("%s/repos/%s/%s/releases/latest", APIBase(), owner, repo))
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var rel Release
	if err := json.NewDecoder(resp.Body).Decode(&rel); err != nil {
		return nil, err
	}
	if rel.Tag == "" {
		return nil, fmt.Errorf("release of %s/%s has no tag", owner, repo)
	}
	return &rel, nil
}

var versionRe = regexp.MustCompile(`v?(\d+)\.(\d+)\.(\d+)`)

func parse(s string) (v [3]int, ok bool) {
	m := versionRe.FindStringSubmatch(s)
	if m == nil {
		return v, false
	}
	for i := range v {
		v[i], _ = strconv.Atoi(m[i+1])
	}
	return v, true
}

// IsNewer reports whether latest is higher than current; ok is false when
// either has no MAJOR.MINOR.PATCH.
func IsNewer(current, latest string) (newer, ok bool) {
	c, okC := parse(current)
	l, okL := parse(latest)
	if !okC || !okL {
		return false, false
	}
	for i := range c {
		if c[i] != l[i] {
			return l[i] > c[i], true
		}
	}
	return false, true
}

// AssetName is the release asset for a build flavour on this platform, as
// release.yml names it: gleann-plugin-sound[-<flavor>]-<tag>-<os>-<arch>.tar.gz.
// The plain whisper build has no flavour infix.
func AssetName(binary, flavor, tag string) string {
	infix := ""
	if flavor != "" && flavor != "whisper" {
		infix = "-" + flavor
	}
	return fmt.Sprintf("%s%s-%s-%s-%s.tar.gz", binary, infix, tag, runtime.GOOS, runtime.GOARCH)
}

// pickAsset finds the asset of the same flavour as the running binary. It
// never substitutes another flavour: a "whisper,onnx" build swapped for a
// plain onnx one would silently lose backends.
func pickAsset(rel *Release, binary, flavor string) (Asset, error) {
	want := AssetName(binary, flavor, rel.Tag)
	for _, a := range rel.Assets {
		if a.Name == want {
			return a, nil
		}
	}
	return Asset{}, fmt.Errorf("release %s has no %q build for %s/%s (expected %s)",
		rel.Tag, flavor, runtime.GOOS, runtime.GOARCH, want)
}

func expectedSum(ctx context.Context, rel *Release, asset string) (string, error) {
	for _, a := range rel.Assets {
		n := strings.ToLower(a.Name)
		if n != "checksums.txt" && n != "sha256sums" && n != "sha256sums.txt" {
			continue
		}
		resp, err := get(ctx, a.URL)
		if err != nil {
			return "", err
		}
		defer resp.Body.Close()
		sc := bufio.NewScanner(resp.Body)
		for sc.Scan() {
			f := strings.Fields(sc.Text())
			if len(f) >= 2 && strings.TrimPrefix(f[1], "*") == asset {
				return strings.ToLower(f[0]), nil
			}
		}
	}
	return "", nil
}

// Install downloads the release asset of this flavour and platform, verifies it against
// the published checksums (when there are any) and replaces dest with it. The
// old file is restored on failure. verified reports a checksum match.
func Install(ctx context.Context, rel *Release, binary, flavor, dest string) (verified bool, err error) {
	asset, err := pickAsset(rel, binary, flavor)
	if err != nil {
		return false, err
	}
	resp, err := get(ctx, asset.URL)
	if err != nil {
		return false, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return false, err
	}
	if want, err := expectedSum(ctx, rel, asset.Name); err != nil {
		return false, fmt.Errorf("checksums: %w", err)
	} else if want != "" {
		h := sha256.Sum256(data)
		if got := hex.EncodeToString(h[:]); got != want {
			return false, fmt.Errorf("checksum mismatch for %s: got %s, want %s", asset.Name, got, want)
		}
		verified = true
	}

	tmp := filepath.Join(filepath.Dir(dest), "."+filepath.Base(dest)+".new")
	defer os.Remove(tmp)
	if err := extract(data, binary, tmp); err != nil {
		return false, err
	}
	old := dest + ".old"
	_ = os.Remove(old)
	if err := os.Rename(dest, old); err != nil {
		return false, fmt.Errorf("move old binary aside: %w", err)
	}
	if err := os.Rename(tmp, dest); err != nil {
		_ = os.Rename(old, dest)
		return false, fmt.Errorf("install new binary: %w", err)
	}
	_ = os.Remove(old)
	return verified, nil
}

func extract(archive []byte, binary, dest string) error {
	gz, err := gzip.NewReader(strings.NewReader(string(archive)))
	if err != nil {
		return err
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	for {
		h, err := tr.Next()
		if err == io.EOF {
			return fmt.Errorf("no %s executable in the archive", binary)
		}
		if err != nil {
			return err
		}
		if h.Typeflag != tar.TypeReg || !strings.HasPrefix(filepath.Base(h.Name), binary) {
			continue
		}
		out, err := os.OpenFile(dest, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o755)
		if err != nil {
			return err
		}
		if _, err := io.Copy(out, tr); err != nil {
			out.Close()
			return err
		}
		return out.Close()
	}
}
