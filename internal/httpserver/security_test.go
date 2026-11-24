package httpserver

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/tevfik/gleann-plugin-sound/internal/core"
)

func secServer(token, pluginToken string) *Server {
	s := New(func() (core.Transcriber, error) {
		return &fakeEngine{segs: []core.Segment{{Start: 0, End: time.Second, Text: "x"}}}, nil
	}, "tr", 0)
	s.token = token
	s.pluginToken = pluginToken
	return s
}

func do(h http.Handler, method, path, host, auth string) int {
	req := httptest.NewRequest(method, path, nil)
	if host != "" {
		req.Host = host
	}
	if auth != "" {
		req.Header.Set("Authorization", auth)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec.Code
}

func TestPluginTokenGuardsEveryRouteButHealth(t *testing.T) {
	h := secServer("", "gleann-tok").Handler()
	for _, p := range []string{"/info", "/convert", "/settings", "/models", "/live"} {
		if c := do(h, "GET", p, "127.0.0.1:8767", ""); c != http.StatusUnauthorized {
			t.Errorf("%s without token: %d, want 401", p, c)
		}
		if c := do(h, "GET", p, "127.0.0.1:8767", "Bearer wrong"); c != http.StatusUnauthorized {
			t.Errorf("%s wrong token: %d, want 401", p, c)
		}
		if c := do(h, "GET", p, "127.0.0.1:8767", "Bearer gleann-tok"); c == http.StatusUnauthorized || c == http.StatusForbidden {
			t.Errorf("%s right token: %d", p, c)
		}
	}
	if c := do(h, "GET", "/health", "127.0.0.1:8767", ""); c != http.StatusOK {
		t.Errorf("/health must stay open: %d", c)
	}
}

// The operator's --token keeps working next to the token from gleann.
func TestBothTokensAccepted(t *testing.T) {
	h := secServer("sound-tok", "gleann-tok").Handler()
	for _, tok := range []string{"sound-tok", "gleann-tok"} {
		if c := do(h, "GET", "/info", "localhost:8767", "Bearer "+tok); c != http.StatusOK {
			t.Errorf("token %q: %d", tok, c)
		}
	}
	if c := do(h, "GET", "/info", "localhost:8767", "Bearer nope"); c != http.StatusUnauthorized {
		t.Errorf("bad token: %d", c)
	}
}

func TestLoopbackHostRequired(t *testing.T) {
	// Without a token, and with a wrong one: a foreign Host on a loopback-bound
	// server is refused (a valid token lifts the check, see TestValidTokenSkipsHostCheck).
	for _, s := range []*Server{secServer("", ""), secServer("", "t")} {
		h := s.Handler()
		if c := do(h, "GET", "/info", "evil.example.com", "Bearer wrong"); c != http.StatusForbidden {
			t.Errorf("foreign Host: %d, want 403", c)
		}
		if c := do(h, "GET", "/info", "[::1]:8767", "Bearer t"); c != http.StatusOK {
			t.Errorf("[::1] Host: %d, want 200", c)
		}
	}
	if c := do(secServer("", "").Handler(), "GET", "/health", "evil.example.com", ""); c != http.StatusOK {
		t.Errorf("/health is exempt from the Host check: %d", c)
	}
}

func TestNoTokenKeepsOldBehaviour(t *testing.T) {
	h := secServer("", "").Handler()
	if c := do(h, "GET", "/info", "127.0.0.1:8767", ""); c != http.StatusOK {
		t.Fatalf("no token configured: /info = %d, want 200", c)
	}
}

func TestPluginTokenAllowsNetworkListen(t *testing.T) {
	s := secServer("", "gleann-tok")
	if err := s.SetListen("0.0.0.0", "", 0); err != nil {
		t.Fatalf("a plugin token protects a non-loopback listener: %v", err)
	}
	if err := secServer("", "").SetListen("0.0.0.0", "", 0); err == nil {
		t.Fatal("non-loopback without any token must be refused")
	}
}

func TestConvertRejectsOverlongMedia(t *testing.T) {
	s := secServer("", "")
	s.SetMaxDuration(4 * time.Hour)
	probeDuration = func(context.Context, string) (time.Duration, error) { return 13 * time.Hour, nil }
	defer func() { probeDuration = core.ProbeDuration }()
	ts := httptest.NewServer(s.Handler())
	defer ts.Close()

	resp := uploadNamed(t, ts.URL, "long.wav", tinyWAV(), "")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusRequestEntityTooLarge {
		t.Fatalf("13h upload: status %d, want 413", resp.StatusCode)
	}

	probeDuration = func(context.Context, string) (time.Duration, error) { return 3 * time.Hour, nil }
	resp2 := uploadNamed(t, ts.URL, "ok.wav", tinyWAV(), "")
	defer resp2.Body.Close()
	if resp2.StatusCode != http.StatusOK {
		t.Fatalf("3h upload: status %d, want 200", resp2.StatusCode)
	}

	// 0 disables the cap.
	s.SetMaxDuration(0)
	probeDuration = func(context.Context, string) (time.Duration, error) { return 13 * time.Hour, nil }
	resp3 := uploadNamed(t, ts.URL, "x.wav", tinyWAV(), "")
	defer resp3.Body.Close()
	if resp3.StatusCode != http.StatusOK {
		t.Fatalf("cap disabled: status %d, want 200", resp3.StatusCode)
	}
}

// A real ffprobe on a real (tiny) file: 0.1 s passes a 1 h cap, fails a 10 ms cap.
func TestConvertDurationCapWithRealProbe(t *testing.T) {
	if _, err := core.ProbeDuration(context.Background(), "/nonexistent"); err != nil && !isProbeUsable(err) {
		t.Skip("no ffprobe/ffmpeg")
	}
	s := secServer("", "")
	s.SetMaxDuration(10 * time.Millisecond)
	ts := httptest.NewServer(s.Handler())
	defer ts.Close()
	resp := uploadNamed(t, ts.URL, "a.wav", tinyWAV(), "")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusRequestEntityTooLarge {
		t.Fatalf("0.1s file with a 10ms cap: status %d, want 413", resp.StatusCode)
	}
}

func isProbeUsable(err error) bool { return err != nil && err.Error() != "" && !isNotFound(err) }

func isNotFound(err error) bool { return errors.Is(err, exec.ErrNotFound) }

// F2: the query token is for /live only.
func TestQueryTokenOnlyOnLive(t *testing.T) {
	h := secServer("", "gleann-tok").Handler()
	for _, p := range []string{"/info", "/settings", "/models", "/convert"} {
		if c := do(h, "GET", p+"?token=gleann-tok", "127.0.0.1:8767", ""); c != http.StatusUnauthorized {
			t.Errorf("%s with ?token=: %d, want 401", p, c)
		}
	}
	if c := do(h, "GET", "/live?token=gleann-tok", "127.0.0.1:8767", ""); c == http.StatusUnauthorized {
		t.Errorf("/live with ?token= must authenticate, got %d", c)
	}
}

// F4: a valid token makes the Host check moot (reverse proxy); no token keeps it.
func TestValidTokenSkipsHostCheck(t *testing.T) {
	h := secServer("", "t").Handler()
	if c := do(h, "GET", "/info", "sound.example.ts.net", "Bearer t"); c != http.StatusOK {
		t.Errorf("proxy Host with valid token: %d, want 200", c)
	}
	if c := do(h, "GET", "/info", "sound.example.ts.net", "Bearer bad"); c != http.StatusForbidden {
		t.Errorf("proxy Host with bad token: %d, want 403", c)
	}
	if c := do(h, "GET", "/info", "sound.example.ts.net", ""); c != http.StatusForbidden {
		t.Errorf("proxy Host without token: %d, want 403", c)
	}
	if c := do(secServer("", "").Handler(), "GET", "/info", "sound.example.ts.net", ""); c != http.StatusForbidden {
		t.Errorf("no token configured, foreign Host: %d, want 403", c)
	}
}

func TestLoopbackHostCaseAndTrailingDot(t *testing.T) {
	h := secServer("", "").Handler()
	for _, host := range []string{"LOCALHOST:8767", "Localhost", "localhost.:8767", "localhost."} {
		if c := do(h, "GET", "/info", host, ""); c != http.StatusOK {
			t.Errorf("Host %q: %d, want 200", host, c)
		}
	}
	if c := do(h, "GET", "/info", "evil.localhost.example", ""); c != http.StatusForbidden {
		t.Errorf("lookalike Host: %d, want 403", c)
	}
}

// F7: the Bearer scheme is case-insensitive.
func TestBearerSchemeCaseInsensitive(t *testing.T) {
	h := secServer("", "tok").Handler()
	for _, a := range []string{"Bearer tok", "bearer tok", "BEARER tok"} {
		if c := do(h, "GET", "/info", "localhost:1", a); c != http.StatusOK {
			t.Errorf("%q: %d, want 200", a, c)
		}
	}
	for _, a := range []string{"Bearer", "Bearer ", "Basic tok", "bearertok"} {
		if c := do(h, "GET", "/info", "localhost:1", a); c != http.StatusUnauthorized {
			t.Errorf("%q: %d, want 401", a, c)
		}
	}
}

// F3/R1: the duration probe has its own timeout and a hung probe is a 422, not a
// silent pass. The real core.ProbeDuration runs against fake ffprobe/ffmpeg
// executables that just sleep: a killed process returns "signal: killed", not a
// context error.
func TestDurationProbeHasOwnTimeout(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"ffprobe", "ffmpeg"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("#!/bin/sh\nexec sleep 30\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	old := probeTimeout
	probeTimeout = 200 * time.Millisecond
	defer func() { probeTimeout = old }()
	probeDuration = core.ProbeDuration

	s := secServer("", "")
	s.SetMaxDuration(time.Hour)
	start := time.Now()
	status, msg := s.checkDuration(context.Background(), "x.wav")
	if status != http.StatusUnprocessableEntity {
		t.Fatalf("hung ffprobe: status %d %q, want 422", status, msg)
	}
	if time.Since(start) > 10*time.Second {
		t.Fatalf("probe was not bounded by its own timeout: %v", time.Since(start))
	}

	// Without ffprobe the ffmpeg -i fallback is bounded the same way.
	if err := os.Remove(filepath.Join(dir, "ffprobe")); err != nil {
		t.Fatal(err)
	}
	if _, err := exec.LookPath("ffprobe"); err == nil {
		t.Skip("a real ffprobe is still on PATH")
	}
	if status, msg := s.checkDuration(context.Background(), "x.wav"); status != http.StatusUnprocessableEntity {
		t.Fatalf("hung ffmpeg fallback: status %d %q, want 422", status, msg)
	}
}

// A client that went away is not a probe timeout and not a pass.
func TestDurationProbeClientGone(t *testing.T) {
	probeDuration = func(ctx context.Context, _ string) (time.Duration, error) {
		<-ctx.Done()
		return 0, errors.New("signal: killed")
	}
	defer func() { probeDuration = core.ProbeDuration }()
	s := secServer("", "")
	s.SetMaxDuration(time.Hour)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if status, _ := s.checkDuration(ctx, "x.wav"); status == 0 {
		t.Fatal("a cancelled request must not pass the duration check")
	}
}

// R4: without a token, /live refuses a browser Origin that is not loopback.
func TestLiveRejectsForeignOriginWithoutToken(t *testing.T) {
	h := secServer("", "").Handler()
	try := func(origin string) int {
		req := httptest.NewRequest("GET", "/live", nil)
		req.Host = "localhost:8767"
		if origin != "" {
			req.Header.Set("Origin", origin)
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec.Code
	}
	for _, o := range []string{"https://evil.example.com", "http://evil.example.com:8767", "null", "http://localhost.evil.com"} {
		if c := try(o); c != http.StatusForbidden {
			t.Errorf("Origin %q: %d, want 403", o, c)
		}
	}
	for _, o := range []string{"", "http://localhost:3000", "http://127.0.0.1:8767", "http://[::1]:8767"} {
		if c := try(o); c == http.StatusForbidden {
			t.Errorf("Origin %q must pass the origin check, got 403", o)
		}
	}
	// With a token the token is the defence; a foreign Origin is not refused.
	ht := secServer("", "t").Handler()
	req := httptest.NewRequest("GET", "/live", nil)
	req.Host = "localhost:8767"
	req.Header.Set("Origin", "https://app.example.com")
	req.Header.Set("Authorization", "Bearer t")
	rec := httptest.NewRecorder()
	ht.ServeHTTP(rec, req)
	if rec.Code == http.StatusForbidden || rec.Code == http.StatusUnauthorized {
		t.Errorf("token + foreign Origin: %d", rec.Code)
	}
}

type decodeEngine struct{ fakeEngine }

func (decodeEngine) TranscribeFile(ctx context.Context, path string) ([]core.Segment, error) {
	if _, err := core.DecodeFile(ctx, path); err != nil {
		return nil, fmt.Errorf("fake: %w", err)
	}
	return []core.Segment{{End: time.Second, Text: "x"}}, nil
}

type decodeRefiner struct{}

func (decodeRefiner) Refine(context.Context, []int16) (string, error) { return "x", nil }
func (decodeRefiner) Close() error                                    { return nil }

// lyingMP3 builds the review's input: a short high-bitrate mp3 followed by a
// long low-bitrate one without a Xing header. ffprobe estimates the duration
// from the first frames and reports a few seconds; decoding yields minutes.
func lyingMP3(t *testing.T) []byte {
	t.Helper()
	dir := t.TempDir()
	gen := func(name, dur, br string) []byte {
		out := filepath.Join(dir, name)
		cmd := exec.Command("ffmpeg", "-v", "error", "-f", "lavfi", "-i", "sine=frequency=440:sample_rate=16000",
			"-t", dur, "-b:a", br, "-write_xing", "0", "-id3v2_version", "0", "-write_id3v1", "0", out)
		if b, err := cmd.CombinedOutput(); err != nil {
			t.Skipf("ffmpeg cannot build the test input: %v %s", err, b)
		}
		b, _ := os.ReadFile(out)
		return b
	}
	return append(gen("short.mp3", "2", "320k"), gen("long.mp3", "600", "8k")...)
}

// F1: a file whose declared duration is short but whose audio is long is
// stopped by the decode-side cap (413), on the engine path and the refiner path.
func TestConvertDecodeCapStopsLyingFile(t *testing.T) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("no ffmpeg")
	}
	if _, err := exec.LookPath("ffprobe"); err != nil {
		t.Skip("no ffprobe")
	}
	media := lyingMP3(t)
	declared, err := core.ProbeDuration(context.Background(), writeTemp(t, "lie.mp3", media))
	if err != nil || declared > 60*time.Second {
		t.Skipf("ffprobe does not underestimate this input here (%v, %v)", declared, err)
	}
	for _, refine := range []bool{false, true} {
		s := New(func() (core.Transcriber, error) { return &decodeEngine{}, nil }, "tr", 0)
		s.SetMaxDuration(60 * time.Second)
		if refine {
			s.SetRefiner(decodeRefiner{})
		}
		ts := httptest.NewServer(s.Handler())
		resp := uploadNamed(t, ts.URL, "lie.mp3", media, "")
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		ts.Close()
		if resp.StatusCode != http.StatusRequestEntityTooLarge {
			t.Errorf("refiner=%v: status %d (%s), want 413", refine, resp.StatusCode, body)
		}
		if !strings.Contains(string(body), "limit") {
			t.Errorf("refiner=%v: message does not name the limit: %s", refine, body)
		}
	}
}

func writeTemp(t *testing.T, name string, b []byte) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(p, b, 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestDefaultMaxDurationFitsRequestTimeout(t *testing.T) {
	if DefaultMaxDuration != 2*time.Hour {
		t.Errorf("DefaultMaxDuration = %v, want 2h", DefaultMaxDuration)
	}
	if DefaultMaxDuration >= requestTimeout*10 {
		t.Errorf("default cap %v too long for the %v request deadline", DefaultMaxDuration, requestTimeout)
	}
}
