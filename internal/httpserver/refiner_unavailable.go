package httpserver

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/tevfik/gleann-plugin-sound/internal/core"
)

// S2: the refiner supervises its own sidecar. While that sidecar is being
// restarted (or has been given up on) /convert is temporarily unable to
// transcribe: that is the plugin's state, not a fault in the upload, so it is
// answered 503 with Retry-After, in the same JSON shape as the
// dependency_missing refusal.

// refinerRetryAfter is the seconds the refiner's supervision wants the client
// to wait, falling back to core.RetryAfter for a refiner that cannot say.
func (s *Server) refinerRetryAfter() int {
	if h, ok := s.refiner.(core.RefinerHealth); ok && h.RetryAfter() > 0 {
		return h.RetryAfter()
	}
	return core.RetryAfter
}

// writeRefinerUnavailable answers 503 + Retry-After with code
// "refiner_unavailable".
func writeRefinerUnavailable(w http.ResponseWriter, msg string, secs int) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Retry-After", strconv.Itoa(secs))
	w.WriteHeader(http.StatusServiceUnavailable)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"error":       msg,
		"code":        "refiner_unavailable",
		"retry_after": secs,
	})
}

// refinerRefused answers a /convert that the refiner's supervision turned
// away (its process died and the replacement is not ready). It reports
// whether err was such a refusal.
func (s *Server) refinerRefused(w http.ResponseWriter, err error) bool {
	if !errors.Is(err, core.ErrRefinerUnavailable) {
		return false
	}
	secs := s.refinerRetryAfter()
	if h, ok := s.refiner.(core.RefinerHealth); ok && h.Status() == "down" {
		writeRefinerUnavailable(w, fmt.Sprintf("transcription failed: %v; the refiner is down, restart the plugin to recover", err), secs)
		return true
	}
	writeRefinerUnavailable(w, fmt.Sprintf("transcription failed: %v; retry in %d s", err, secs), secs)
	return true
}

// refinerDownNow answers a /convert whose refiner has given up restarting,
// before the upload is decoded (down is terminal: only a plugin restart
// recovers, hence the long Retry-After), so no CPU is spent on a recording that cannot
// be transcribed. It reports whether it answered.
func (s *Server) refinerDownNow(w http.ResponseWriter) bool {
	h, ok := s.refiner.(core.RefinerHealth)
	if !ok || strings.TrimSpace(h.Status()) != "down" {
		return false
	}
	secs := s.refinerRetryAfter()
	writeRefinerUnavailable(w, "asr refiner is down and is not restarting; restart the plugin to recover", secs)
	return true
}
