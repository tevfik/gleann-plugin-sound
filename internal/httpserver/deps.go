package httpserver

import (
	"context"
	"os/exec"
	"strings"
	"sync"
	"time"
)

// Dependency advertising (S4): /health reports whether ffmpeg and ffprobe
// exist and which version they are, so gleann and operators can tell a
// dependency problem from a bad upload, and /convert can answer 503 with an
// install hint instead of a generic 500 when ffmpeg is absent.

// depProbeTTL is how long a probe result is reused, so /health stays fast
// even though `ffmpeg -version` forks a process. A variable so tests can
// force refreshes.
var depProbeTTL = 60 * time.Second

// lookPath and depVersion are the probe seams: tests replace them to fake a
// machine with or without ffmpeg without touching $PATH globally.
var lookPath = exec.LookPath
var depVersion = defaultDepVersion

// defaultDepVersion returns the first line of `ffmpeg -version`.
func defaultDepVersion(ctx context.Context, name string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, name, "-version").Output()
	if err != nil {
		return "", err
	}
	line, _, _ := strings.Cut(strings.TrimSpace(string(out)), "\n")
	if len(line) > 120 {
		line = strings.ToValidUTF8(line[:120], "")
	}
	return line, nil
}

// depReport is one dependency's state as advertised by /health.
type depReport struct {
	OK      bool   `json:"ok"`
	Version string `json:"version,omitempty"`
	Error   string `json:"error,omitempty"`
}

// depsCache memoises the probe for one server.
type depsCache struct {
	mu  sync.Mutex
	at  time.Time
	rep map[string]*depReport
}

// dependencies probes ffmpeg and ffprobe (LookPath plus version first line),
// reusing the previous result while it is younger than depProbeTTL.
func (s *Server) dependencies(ctx context.Context) map[string]*depReport {
	s.deps.mu.Lock()
	defer s.deps.mu.Unlock()
	if s.deps.rep != nil && time.Since(s.deps.at) < depProbeTTL {
		return s.deps.rep
	}
	rep := make(map[string]*depReport, 2)
	for _, bin := range [2]string{"ffmpeg", "ffprobe"} {
		r := &depReport{}
		if _, err := lookPath(bin); err != nil {
			r.Error = "not found in PATH"
		} else {
			r.OK = true
			if v, verr := depVersion(ctx, bin); verr == nil {
				r.Version = v
			}
		}
		rep[bin] = r
	}
	s.deps.rep, s.deps.at = rep, time.Now()
	return rep
}
