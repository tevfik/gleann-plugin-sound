// Package httpserver implements the HTTP plugin server for gleann integration.
//
// It implements the gleann plugin contract:
//
//	GET  /health  → {"status":"ok","plugin":"gleann-plugin-sound","capabilities":[...]}
//	GET  /info    → name, version, schema_version, capabilities, extensions
//	POST /convert → multipart "file"; returns {"markdown","text","segments","language"}
//
// The plugin advertises "audio-transcription", which is how current gleann
// routes audio (GLEANN_AUDIO_PLUGIN_URL, used with --multimodal-model), and
// "document-extraction" for older gleann versions. Files with no speech get
// HTTP 422 so gleann skips them instead of indexing an empty transcript.
package httpserver

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/tevfik/gleann-plugin-sound/internal/audio"
	"github.com/tevfik/gleann-plugin-sound/internal/config"
	"github.com/tevfik/gleann-plugin-sound/internal/core"
	"github.com/tevfik/gleann-plugin-sound/internal/pipeline"
	"github.com/tevfik/gleann/modules/settings"
)

// PluginVersion is reported by /info and compared by `update`. main replaces
// it with the release tag stamped in by -ldflags "-X main.version=...".
var PluginVersion = "1.0.0"

const (
	// PluginName is the registry name.
	PluginName = "gleann-plugin-sound"
	// SchemaVersion is the gleann plugin protocol version this server speaks.
	SchemaVersion = 2

	// requestTimeout bounds a single transcription (RTF is ~0.2, so this
	// covers recordings of several hours).
	requestTimeout = 30 * time.Minute
)

// Capabilities advertised to gleann.
var Capabilities = []string{"audio-transcription", "document-extraction"}

// SupportedExts lists the media types accepted by /convert (anything ffmpeg reads).
var SupportedExts = []string{
	".mp3", ".wav", ".m4a", ".flac", ".ogg", ".opus", ".aac", ".wma",
	".webm", ".mp4", ".mkv", ".avi",
}

// decodeStatus maps a decode failure to an HTTP status: a missing ffmpeg is a
// server problem (500), anything else means the uploaded file is not usable
// audio (422).
func decodeStatus(err error) int {
	if errors.Is(err, exec.ErrNotFound) {
		return http.StatusInternalServerError
	}
	return http.StatusUnprocessableEntity
}

// EngineFactory creates a Transcriber on demand. Used for lazy model loading.
type EngineFactory func() (core.Transcriber, error)

// Server is a lightweight HTTP plugin server for gleann document extraction.
type Server struct {
	factory  EngineFactory
	language string
	port     int

	corrector *core.Corrector
	refiner   core.Refiner // optional second pass for /live utterances
	speakers  core.SpeakerLabeler
	openVAD   func() (core.VADProvider, func()) // per-session voice activity detector

	host     string // listen address; loopback unless set
	token    string // required by /live when non-empty
	liveSlot chan struct{}

	// Lazy-loaded engine with mutex for thread safety.
	mu     sync.Mutex
	engine core.Transcriber
}

// SetVADFactory overrides how /live sessions get their voice activity detector
// (default: audio.OpenVAD, i.e. Silero when its model is present).
func (s *Server) SetVADFactory(f func() (core.VADProvider, func())) { s.openVAD = f }

// SetSpeakers makes /live label each utterance with its speaker.
func (s *Server) SetSpeakers(l core.SpeakerLabeler) { s.speakers = l }

// SetRefiner enables background re-transcription of /live utterances.
func (s *Server) SetRefiner(r core.Refiner) { s.refiner = r }

// SetCorrector installs word corrections applied to every transcript.
func (s *Server) SetCorrector(c *core.Corrector) { s.corrector = c }

// New creates a new plugin HTTP server.
// The engine is loaded lazily on the first /convert request so that the
// health endpoint responds immediately (within the PluginManager's 10s timeout).
func New(factory EngineFactory, language string, port int) *Server {
	return &Server{
		factory:  factory,
		language: language,
		port:     port,
		host:     "127.0.0.1",
		openVAD:  audio.OpenVAD,
		liveSlot: make(chan struct{}, DefaultMaxLive),
	}
}

// SetListen sets the bind address. Anything but loopback requires a token,
// because /live exposes the microphone path to the network.
func (s *Server) SetListen(host, token string, maxLive int) error {
	if host == "" {
		host = "127.0.0.1"
	}
	if token == "" && !isLoopback(host) {
		return fmt.Errorf("refusing to listen on %q without an access token (--token)", host)
	}
	s.host, s.token = host, token
	if maxLive > 0 {
		s.liveSlot = make(chan struct{}, maxLive)
	}
	return nil
}

// Handler returns the HTTP handler (used by Serve and by tests).
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/health", s.handleHealth)
	mux.HandleFunc("/info", s.handleInfo)
	mux.HandleFunc("/convert", s.handleConvert)
	mux.HandleFunc("/live", s.handleLive)
	mux.HandleFunc("/settings", s.handleSettings)
	// Model downloads for the same clients; they outlive the request.
	mux.Handle("/models", config.Catalog().Handler(context.Background(), s.authorized))
	return mux
}

// Serve starts the HTTP server. Blocks until the server is shut down.
func (s *Server) Serve() error {
	mux := http.NewServeMux()
	mux.Handle("/", s.Handler())

	addr := net.JoinHostPort(s.host, fmt.Sprint(s.port))
	log.Printf("[plugin-serve] gleann-plugin-sound HTTP plugin on %s", addr)
	return http.ListenAndServe(addr, mux)
}

// Close releases the transcription engine if it was loaded.
func (s *Server) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.engine != nil {
		return s.engine.Close()
	}
	return nil
}

// getEngine returns the transcription engine, initialising it on first call.
func (s *Server) getEngine() (core.Transcriber, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.engine != nil {
		return s.engine, nil
	}
	log.Println("[plugin-serve] loading transcription model (first request)...")
	engine, err := s.factory()
	if err != nil {
		return nil, err
	}
	if s.language != "" {
		engine.SetLanguage(s.language)
	}
	s.engine = engine
	log.Println("[plugin-serve] model loaded successfully")
	return s.engine, nil
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"status":         "ok",
		"plugin":         PluginName,
		"version":        PluginVersion,
		"schema_version": SchemaVersion,
		"capabilities":   Capabilities,
	})
}

func (s *Server) handleInfo(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"name":           PluginName,
		"version":        PluginVersion,
		"schema_version": SchemaVersion,
		"capabilities":   Capabilities,
		"extensions":     SupportedExts,
		// Generic settings form for clients such as the gleann web UI.
		"settings": map[string]any{"path": "/settings", "schema_version": settings.SchemaVersion},
	})
}

func (s *Server) handleConvert(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "only POST is supported")
		return
	}

	// Parse multipart form — 256 MB max (enough for most audio/video files).
	if err := r.ParseMultipartForm(256 << 20); err != nil {
		writeError(w, http.StatusBadRequest, fmt.Sprintf("parse multipart: %v", err))
		return
	}

	file, header, err := r.FormFile("file")
	if err != nil {
		writeError(w, http.StatusBadRequest, fmt.Sprintf("missing 'file' field: %v", err))
		return
	}
	defer file.Close()

	// Save to temp file preserving extension (ffmpeg needs it for format detection).
	ext := filepath.Ext(header.Filename)
	tmp, err := os.CreateTemp("", "gleann-plugin-sound-*"+ext)
	if err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Sprintf("create temp file: %v", err))
		return
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)

	if _, err := io.Copy(tmp, file); err != nil {
		tmp.Close()
		writeError(w, http.StatusInternalServerError, fmt.Sprintf("write temp file: %v", err))
		return
	}
	tmp.Close()

	ctx, cancel := context.WithTimeout(r.Context(), requestTimeout)
	defer cancel()

	var segments []core.Segment
	if s.refiner != nil {
		// Offline path: Silero finds the speech, speaker turns are split and
		// the accurate model transcribes each piece (no streaming model needed).
		pcm, err := core.DecodeFile(ctx, tmpPath)
		if err != nil {
			writeError(w, decodeStatus(err), fmt.Sprintf("decode: %v", err))
			return
		}
		vad, closeVAD := audio.OpenVAD()
		segments, err = pipeline.TranscribeFile(ctx, pcm, vad, s.refiner,
			pipeline.FileConfig{Corrector: s.corrector, Speakers: s.speakers})
		closeVAD()
		if err != nil {
			writeError(w, http.StatusInternalServerError, fmt.Sprintf("transcription failed: %v", err))
			return
		}
	} else {
		// Get engine (lazy load on first request).
		engine, err := s.getEngine()
		if err != nil {
			writeError(w, http.StatusInternalServerError, fmt.Sprintf("load model: %v", err))
			return
		}
		// Hold the mutex so concurrent requests are serialised
		// (whisper engine is NOT thread-safe).
		s.mu.Lock()
		segments, err = engine.TranscribeFile(ctx, tmpPath)
		s.mu.Unlock()
		if err != nil {
			writeError(w, http.StatusInternalServerError, fmt.Sprintf("transcription failed: %v", err))
			return
		}
	}

	if len(segments) == 0 {
		writeError(w, http.StatusUnprocessableEntity, "no speech detected")
		return
	}

	type jsonSeg struct {
		Start float64 `json:"start"`
		End   float64 `json:"end"`
		Text  string  `json:"text"`
		// Speaker is set when voices are enrolled ("?" = unknown).
		Speaker string `json:"speaker,omitempty"`
	}
	segs := make([]jsonSeg, 0, len(segments))
	texts := make([]string, 0, len(segments))
	for _, sg := range segments {
		t := strings.TrimSpace(s.corrector.Apply(sg.Text))
		if t == "" {
			continue
		}
		segs = append(segs, jsonSeg{Start: sg.Start.Seconds(), End: sg.End.Seconds(), Text: t, Speaker: sg.Speaker})
		texts = append(texts, t)
	}
	if len(texts) == 0 {
		writeError(w, http.StatusUnprocessableEntity, "no speech detected")
		return
	}
	corrected := make([]core.Segment, len(segs))
	for i, sg := range segs {
		corrected[i] = core.Segment{
			Start: time.Duration(sg.Start * float64(time.Second)),
			End:   time.Duration(sg.End * float64(time.Second)),
			Text:  sg.Text, Speaker: sg.Speaker,
		}
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"schema_version": SchemaVersion,
		"markdown":       formatMarkdown(header.Filename, corrected),
		"text":           strings.Join(texts, " "),
		"language":       s.language,
		"segments":       segs,
	})
}

// formatMarkdown converts transcription segments into a readable markdown document.
func formatMarkdown(filename string, segments []core.Segment) string {
	if len(segments) == 0 {
		return ""
	}

	var b strings.Builder
	b.WriteString(fmt.Sprintf("# Transcription: %s\n\n", filename))

	var fullText []string
	for _, seg := range segments {
		start := formatTimestamp(seg.Start)
		end := formatTimestamp(seg.End)
		text := strings.TrimSpace(seg.Text)
		if text == "" {
			continue
		}
		who := ""
		if seg.Speaker != "" {
			who = seg.Speaker + ": "
		}
		b.WriteString(fmt.Sprintf("[%s - %s] %s%s\n\n", start, end, who, text))
		fullText = append(fullText, text)
	}

	// Append a full-text block for better semantic chunking.
	if len(fullText) > 0 {
		b.WriteString("---\n\n")
		b.WriteString(strings.Join(fullText, " "))
		b.WriteString("\n")
	}

	return b.String()
}

// formatTimestamp converts a duration to MM:SS format.
func formatTimestamp(d time.Duration) string {
	total := int(d.Seconds())
	m := total / 60
	s := total % 60
	return fmt.Sprintf("%02d:%02d", m, s)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": msg})
}
