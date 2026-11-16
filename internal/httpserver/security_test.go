package httpserver

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os/exec"
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
	// With and without a token: a foreign Host on a loopback-bound server is refused.
	for _, s := range []*Server{secServer("", ""), secServer("", "t")} {
		h := s.Handler()
		if c := do(h, "GET", "/info", "evil.example.com", "Bearer t"); c != http.StatusForbidden {
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
