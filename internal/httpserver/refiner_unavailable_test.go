package httpserver

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/tevfik/gleann-plugin-sound/internal/core"
)

// stubRefiner reports a supervision state the way tcasr.Refiner does.
type stubRefiner struct {
	status string
	secs   int
}

func (s stubRefiner) Refine(context.Context, []int16) (string, error) { return "refined", nil }
func (s stubRefiner) Close() error                                    { return nil }
func (s stubRefiner) Status() string                                  { return s.status }
func (s stubRefiner) RetryAfter() int                                 { return s.secs }

// /health carries the supervision's own state, including "restarting".
func TestHealthReportsRefinerSupervisionState(t *testing.T) {
	for _, st := range []string{"up", "restarting", "down"} {
		s := New(nil, "en", 0)
		s.SetRefiner(stubRefiner{status: st})
		ts := httptest.NewServer(s.Handler())
		if got := healthField(t, ts, "refiner"); got != st {
			t.Errorf("refiner = %q, want %q", got, st)
		}
		ts.Close()
	}
}

// The upload is not decoded when the refiner has given up restarting.
func TestConvertRefinerDownRefusedBeforeDecode(t *testing.T) {
	s := New(func() (core.Transcriber, error) { return &fakeEngine{}, nil }, "tr", 0)
	s.SetRefiner(stubRefiner{status: "down", secs: 7})
	ts := httptest.NewServer(s.Handler())
	defer ts.Close()
	resp := uploadNamed(t, ts.URL, "a.wav", tinyWAV(), "")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503 while the refiner is down", resp.StatusCode)
	}
	if got := resp.Header.Get("Retry-After"); got != "7" {
		t.Errorf("Retry-After = %q, want 7", got)
	}
}

// Mid-flight refusal: the refiner said "up" and its supervision refused anyway.
func TestConvertRefinerUnavailableIs503(t *testing.T) {
	s := New(nil, "tr", 0)
	s.SetRefiner(stubRefiner{status: "restarting", secs: 9})
	rec := httptest.NewRecorder()
	err := fmt.Errorf("refine 0:00:01.5: %w", core.ErrRefinerUnavailable)
	if !s.refinerRefused(rec, err) {
		t.Fatal("ErrRefinerUnavailable was not recognised")
	}
	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("code = %d, want 503", rec.Code)
	}
	if got := rec.Header().Get("Retry-After"); got != "9" {
		t.Errorf("Retry-After = %q, want 9", got)
	}
	if b := rec.Body.String(); !strings.Contains(b, "refiner_unavailable") || !strings.Contains(b, `"retry_after":9`) {
		t.Errorf("body = %s", b)
	}
	rec2 := httptest.NewRecorder()
	if s.refinerRefused(rec2, errors.New("bad helper reply")) || s.refinerRefused(rec2, nil) {
		t.Error("an unrelated error was answered as a refusal")
	}
}

// A refiner that cannot name a back-off still gets core.RetryAfter.
func TestRefinerRetryAfterFallsBack(t *testing.T) {
	s := New(nil, "tr", 0)
	s.SetRefiner(&recordingRefiner{})
	rec := httptest.NewRecorder()
	if !s.refinerRefused(rec, core.ErrRefinerUnavailable) || rec.Header().Get("Retry-After") != "5" {
		t.Errorf("Retry-After = %q, want %d", rec.Header().Get("Retry-After"), core.RetryAfter)
	}
}

// M1: the tc engine path (no refiner configured) maps a supervised helper's
// death to 503 + Retry-After too, not 500.
func TestConvertEngineUnavailableIs503(t *testing.T) {
	s := New(func() (core.Transcriber, error) {
		return &failingEngine{err: fmt.Errorf("pass: %w", core.ErrRefinerUnavailable)}, nil
	}, "tr", 0)
	ts := httptest.NewServer(s.Handler())
	defer ts.Close()
	resp := uploadNamed(t, ts.URL, "a.wav", tinyWAV(), "")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusServiceUnavailable || resp.Header.Get("Retry-After") != "5" {
		t.Errorf("status = %d, Retry-After = %q, want 503 and %d", resp.StatusCode, resp.Header.Get("Retry-After"), core.RetryAfter)
	}
}

type failingEngine struct {
	fakeEngine
	err error
}

func (f *failingEngine) TranscribeFile(context.Context, string) ([]core.Segment, error) {
	return nil, f.err
}
