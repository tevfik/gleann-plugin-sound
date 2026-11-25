package httpserver

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/tevfik/gleann-plugin-sound/internal/core"
)

// S4: /health must be honest about what the server actually can do. The
// gleann PluginManager keeps its 120 s ProcessStructured timeout unless the
// plugin advertises a longer one, and operators need to see why /convert
// fails when ffmpeg is absent or the refiner's sidecar died.
func TestHealthAdvertisesTimeoutDepsRefinerModel(t *testing.T) {
	oldLook, oldVer, oldTTL := lookPath, depVersion, depProbeTTL
	defer func() { lookPath, depVersion, depProbeTTL = oldLook, oldVer, oldTTL }()
	lookPath = func(name string) (string, error) { return "/usr/bin/" + name, nil }
	depVersion = func(_ context.Context, name string) (string, error) {
		return name + " version 7.1.1-fake copyright", nil
	}
	depProbeTTL = 0

	s := New(func() (core.Transcriber, error) {
		return &fakeEngine{segs: []core.Segment{{End: time.Second, Text: "x"}}}, nil
	}, "tr", 0)
	ts := httptest.NewServer(s.Handler())
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/health")
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		Status       string `json:"status"`
		Plugin       string `json:"plugin"`
		Version      string `json:"version"`
		SchemaVer    int    `json:"schema_version"`
		Capabilities []string
		Timeout      int `json:"timeout"`
		Deps         map[string]struct {
			OK      bool   `json:"ok"`
			Version string `json:"version"`
		}
		Refiner string `json:"refiner"`
		Model   struct{ Loaded bool }
	}
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()

	// Existing fields must keep working: the host treats any 200 answer as
	// healthy but still reads status/capabilities.
	if got.Status != "ok" || got.Plugin != PluginName || got.SchemaVer != SchemaVersion {
		t.Errorf("existing health fields broken: %+v", got)
	}
	if len(got.Capabilities) == 0 {
		t.Error("capabilities missing")
	}
	// The host raises its client timeout from this. 30 min request deadline
	// must be advertised as 1800 seconds.
	if got.Timeout != 1800 {
		t.Errorf("timeout = %d, want 1800 seconds (requestTimeout=%v)", got.Timeout, requestTimeout)
	}
	for _, bin := range []string{"ffmpeg", "ffprobe"} {
		d, ok := got.Deps[bin]
		if !ok {
			t.Fatalf("deps.%s missing from /health", bin)
		}
		if !d.OK {
			t.Errorf("deps.%s.ok = false with the fake probe", bin)
		}
		if !strings.Contains(d.Version, bin+" version") {
			t.Errorf("deps.%s.version = %q, want the first line of %q", bin, d.Version, bin+" -version")
		}
	}
	if got.Refiner != "off" {
		t.Errorf("refiner = %q, want off when none is configured", got.Refiner)
	}
	if got.Model.Loaded {
		t.Error("model.loaded = true before the first transcription")
	}

	// model.loaded flips once the lazy engine exists.
	up := upload(t, ts.URL)
	up.Body.Close()
	resp, err = http.Get(ts.URL + "/health")
	if err != nil {
		t.Fatal(err)
	}
	_ = json.NewDecoder(resp.Body).Decode(&got)
	resp.Body.Close()
	if !got.Model.Loaded {
		t.Error("model.loaded = false after a successful /convert")
	}

	// refiner states: up when the helper is healthy, down when it died.
	rf := &recordingRefiner{}
	s.SetRefiner(rf)
	if c := healthField(t, ts, "refiner"); c != "up" {
		t.Errorf("refiner = %q, want up with a live helper", c)
	}
	rf.closed.Store(true) // pretends the sidecar died
	if c := healthField(t, ts, "refiner"); c != "down" {
		t.Errorf("refiner = %q, want down after the helper died", c)
	}
}

func healthField(t *testing.T, ts *httptest.Server, key string) string {
	t.Helper()
	resp, err := http.Get(ts.URL + "/health")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var m map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&m); err != nil {
		t.Fatal(err)
	}
	s, _ := m[key].(string)
	return s
}

// /health must stay fast: the version probes run at most once per depProbeTTL.
func TestHealthDepsCached(t *testing.T) {
	oldLook, oldVer, oldTTL := lookPath, depVersion, depProbeTTL
	defer func() { lookPath, depVersion, depProbeTTL = oldLook, oldVer, oldTTL }()
	var looks atomic.Int64
	lookPath = func(string) (string, error) {
		looks.Add(1)
		return "/usr/bin/tool", nil
	}
	depVersion = func(context.Context, string) (string, error) { return "fake 1.0", nil }
	depProbeTTL = time.Hour

	s := New(nil, "en", 0)
	ts := httptest.NewServer(s.Handler())
	defer ts.Close()
	for i := 0; i < 3; i++ {
		resp, err := http.Get(ts.URL + "/health")
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
	}
	if n := looks.Load(); n != 2 { // ffmpeg + ffprobe, exactly one probe per TTL window
		t.Errorf("lookPath calls = %d, want 2 (deps must be cached for depProbeTTL)", n)
	}

	depProbeTTL = 0 // force a refresh: the cache must not outlive the TTL
	resp, err := http.Get(ts.URL + "/health")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if n := looks.Load(); n != 4 {
		t.Errorf("lookPath calls after TTL expiry = %d, want 4", n)
	}
}

// A missing ffmpeg must be honest in /health (deps.ffmpeg.ok=false, no
// version) and turn /convert into a 503 with an install hint and the
// dependency_missing contract code, never a generic 500.
func TestMissingFfmpegHealthAndConvert503(t *testing.T) {
	oldLook, oldTTL := lookPath, depProbeTTL
	defer func() { lookPath, depProbeTTL = oldLook, oldTTL }()
	depProbeTTL = 0
	lookPath = func(name string) (string, error) {
		if name == "ffmpeg" {
			return "", &exec.Error{Name: name, Err: exec.ErrNotFound}
		}
		return "/usr/bin/" + name, nil
	}

	s := New(func() (core.Transcriber, error) { return &fakeEngine{}, nil }, "tr", 0)
	ts := httptest.NewServer(s.Handler())
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/health")
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		Status string `json:"status"`
		Deps   map[string]struct {
			OK      bool   `json:"ok"`
			Version string `json:"version"`
		}
	}
	_ = json.NewDecoder(resp.Body).Decode(&got)
	resp.Body.Close()
	if got.Status != "ok" {
		t.Errorf("status = %q, want ok (the host treats any 200 as healthy)", got.Status)
	}
	if f := got.Deps["ffmpeg"]; f.OK || f.Version != "" {
		t.Errorf("deps.ffmpeg = %+v, want ok=false and no version", f)
	}
	if p := got.Deps["ffprobe"]; !p.OK {
		t.Errorf("deps.ffprobe.ok = false, this fake only removed ffmpeg")
	}

	resp2 := upload(t, ts.URL)
	body := readAll(resp2)
	if resp2.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("/convert with ffmpeg missing: status %d (%s), want 503", resp2.StatusCode, body)
	}
	var e struct {
		Error string `json:"error"`
		Code  string `json:"code"`
	}
	if err := json.Unmarshal(body, &e); err != nil {
		t.Fatalf("503 body %q: %v", body, err)
	}
	if e.Code != "dependency_missing" {
		t.Errorf("code = %q, want dependency_missing (plan 12 contract)", e.Code)
	}
	if !strings.Contains(strings.ToLower(e.Error), "ffmpeg") || !strings.Contains(strings.ToLower(e.Error), "install") {
		t.Errorf("503 body must carry an install hint, got %q", e.Error)
	}
	// The upload must not linger in the staging dir.
	if left, _ := filepath.Glob(filepath.Join(s.uploadDir(), "*")); len(left) != 0 {
		t.Errorf("stale upload left behind: %v", left)
	}
}

// Defense in depth: the validateUpload error shape from core.ValidateUpload
// (a wrapped *exec.Error) must also map to 503, not 500.
func TestDecodeNotFoundWrap503(t *testing.T) {
	err := fmt.Errorf("ffmpeg not available: %w", &exec.Error{Name: "ffmpeg", Err: exec.ErrNotFound})
	if got := decodeStatus(err); got != http.StatusServiceUnavailable {
		t.Errorf("wrapped exec.Error: status %d, want 503", got)
	}
}

func readAll(resp *http.Response) []byte {
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return b
}
