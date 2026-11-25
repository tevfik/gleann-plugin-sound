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
	"sync/atomic"
	"time"

	"github.com/tevfik/gleann-plugin-sound/internal/audio"
	"github.com/tevfik/gleann-plugin-sound/internal/config"
	"github.com/tevfik/gleann-plugin-sound/internal/core"
	"github.com/tevfik/gleann-plugin-sound/internal/pipeline"
	"github.com/tevfik/gleann/modules/settings"
)

// PluginVersion is reported by /info and compared by `update`. main replaces
// it with the release tag stamped in by -ldflags "-X main.version=...".
// "dev" marks a build without a stamped version.
var PluginVersion = "dev"

const (
	// PluginName is the registry name.
	PluginName = "gleann-plugin-sound"
	// SchemaVersion is the gleann plugin protocol version this server speaks.
	SchemaVersion = 2

	// requestTimeout bounds a single transcription (RTF is ~0.2, so this
	// covers recordings of several hours).
	requestTimeout = 30 * time.Minute

	// DefaultMaxDuration is the longest recording /convert accepts unless
	// configured otherwise.
	DefaultMaxDuration = 2 * time.Hour
)

// probeTimeout bounds the duration probe on its own so a pathological file
// cannot spend the whole request deadline there; a variable so tests can
// shorten it.
var probeTimeout = 30 * time.Second

// Capabilities advertised to gleann.
var Capabilities = []string{"audio-transcription", "document-extraction"}

// SupportedExts lists the media types accepted by /convert (anything ffmpeg reads).
var SupportedExts = []string{
	".mp3", ".wav", ".m4a", ".flac", ".ogg", ".opus", ".aac", ".wma",
	".webm", ".mp4", ".mkv", ".avi",
}

// validateUpload is core.ValidateUpload; a variable so tests with a fake engine
// do not need ffmpeg.
var validateUpload = core.ValidateUpload

// probeDuration is core.ProbeDuration; a variable so tests with fake media do
// not need ffprobe.
var probeDuration = core.ProbeDuration

// checkDuration rejects media longer than the configured cap before anything
// is decoded. It returns the HTTP status and message to send, or 0.
func (s *Server) checkDuration(ctx context.Context, path string) (int, string) {
	if s.maxDuration <= 0 {
		return 0, ""
	}
	pctx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()
	d, err := probeDuration(pctx, path)
	// A killed ffprobe/ffmpeg reports "signal: killed", not a context error, so
	// decide from the contexts: the probe's own deadline (422) or a request that
	// is already over (client gone, request deadline).
	if err != nil {
		if ctx.Err() != nil {
			return http.StatusRequestTimeout, "request ended while reading the media duration"
		}
		if pctx.Err() != nil {
			return http.StatusUnprocessableEntity, "timed out reading the media duration"
		}
	}
	switch {
	case err == nil:
		if d > s.maxDuration {
			return http.StatusRequestEntityTooLarge,
				fmt.Sprintf("media is %s long; the limit is %s", d.Round(time.Second), s.maxDuration)
		}
	case errors.Is(err, core.ErrDurationUnknown):
		log.Printf("[plugin-serve] duration of the upload is not declared; the %s cap cannot be checked", s.maxDuration)
	case errors.Is(err, exec.ErrNotFound):
		log.Printf("[plugin-serve] ffprobe/ffmpeg not found; the %s duration cap is not enforced", s.maxDuration)
	default:
		log.Printf("[plugin-serve] cannot probe duration: %v", err)
	}
	return 0, ""
}

// writeTooLong answers 413 when decoding crossed the duration cap although the
// container declared a shorter (or no) duration.
func (s *Server) writeTooLong(w http.ResponseWriter) {
	writeError(w, http.StatusRequestEntityTooLarge,
		fmt.Sprintf("decoded audio is longer than the limit of %s (the file's declared duration was wrong)", s.maxDuration))
}

// maxUploadBytes bounds the request body of /convert.
const maxUploadBytes = 256 << 20

func extSupported(ext string) bool {
	for _, e := range SupportedExts {
		if e == ext {
			return true
		}
	}
	return false
}

// decodeStatus maps a decode failure to an HTTP status: a missing ffmpeg or
// ffprobe is a dependency problem of the plugin host (503 + install hint,
// contract code dependency_missing), anything else means the uploaded file
// is not usable audio (422).
func decodeStatus(err error) int {
	if errors.Is(err, exec.ErrNotFound) {
		return http.StatusServiceUnavailable
	}
	return http.StatusUnprocessableEntity
}

// ffmpegMissingHint is the operator-facing fix for a 503 dependency_missing.
const ffmpegMissingHint = "ffmpeg is not installed on the plugin host: /convert needs ffmpeg and ffprobe. " +
	"Install it (sudo apt install ffmpeg / sudo dnf install ffmpeg / brew install ffmpeg) and retry."

// writeDependencyMissing answers the 503 with the plan-12 contract code.
func writeDependencyMissing(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusServiceUnavailable)
	_ = json.NewEncoder(w).Encode(map[string]string{
		"error": ffmpegMissingHint,
		"code":  "dependency_missing",
	})
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

	host  string // listen address; loopback unless set
	token string // access token (--token); required on every route except /health when non-empty
	// pluginToken is the token gleann generated for this process
	// (GLEANN_PLUGIN_TOKEN); accepted in addition to token.
	pluginToken string
	// maxDuration caps the media length /convert accepts; 0 disables the cap.
	maxDuration time.Duration
	liveSlot    chan struct{}

	// Lazy-loaded engine with mutex for thread safety.
	mu     sync.Mutex
	engine core.Transcriber

	srvMu    sync.Mutex
	srv      *http.Server
	stopped  bool
	closeEng sync.Once
	engErr   error
	// closeRef guarantees the refiner's sidecar helper is released exactly
	// once, on Shutdown or Close, before the process exits (S3).
	closeRef sync.Once
	refErr   error

	// S3 shutdown parity: rootCtx is the BaseContext of every request;
	// Shutdown cancels it when the drain deadline passes, which revokes the
	// contexts of parked handlers (and the ffmpeg children they started with
	// exec.CommandContext) even on hijacked connections.
	rootCtx    context.Context
	rootCancel context.CancelFunc

	// uploads tracks the in-flight /convert temp files so Shutdown can
	// remove those of handlers parked past the drain deadline.
	uploads uploadSet

	// fallbackDir is a private os.MkdirTemp staging dir, used only when the
	// per-user staging dir fails its safety checks.
	fallbackMu  sync.Mutex
	fallbackDir string

	// release bookkeeping: an abandoned (wedged) release is not waited for
	// again by the next caller.
	relMu        sync.Mutex
	relDone      chan error
	relAbandoned bool

	// S4 dependency advertising.
	deps        *depsCache
	modelLoaded atomic.Bool
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
	rootCtx, rootCancel := context.WithCancel(context.Background())
	return &Server{
		factory:    factory,
		language:   language,
		port:       port,
		host:       "127.0.0.1",
		openVAD:    audio.OpenVAD,
		liveSlot:   make(chan struct{}, DefaultMaxLive),
		rootCtx:    rootCtx,
		rootCancel: rootCancel,
		deps:       &depsCache{},
	}
}

// SetListen sets the bind address. Anything but loopback requires a token,
// because /live exposes the microphone path to the network.
func (s *Server) SetListen(host, token string, maxLive int) error {
	if host == "" {
		host = "127.0.0.1"
	}
	if token == "" && s.pluginToken == "" && !isLoopback(host) {
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
	mux.Handle("/models", config.Catalog().Handler(context.Background(), s.authorizedConfig))
	return s.guard(mux)
}

// SetPluginToken sets the token gleann passes in GLEANN_PLUGIN_TOKEN. Call it
// before SetListen. It is accepted alongside the operator's --token.
func (s *Server) SetPluginToken(token string) { s.pluginToken = token }

// SetMaxDuration caps the duration of media accepted by /convert (0 = no cap).
func (s *Server) SetMaxDuration(d time.Duration) { s.maxDuration = d }

// Serve starts the HTTP server. Blocks until the server is shut down.
// A shutdown requested through Shutdown or Close is not an error.
func (s *Server) Serve() error {
	mux := http.NewServeMux()
	mux.Handle("/", s.Handler())

	addr := net.JoinHostPort(s.host, fmt.Sprint(s.port))
	// Listen explicitly (rather than ListenAndServe) so the stale-upload
	// sweep can run the instant the port is ours and before the first
	// connection is accepted.
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return err
	}
	srv := &http.Server{
		Addr:    addr,
		Handler: mux,
		// BaseContext roots every request context in rootCtx: Shutdown
		// revokes rootCtx at the drain deadline, cancelling handlers that
		// outlived the drain — including hijacked /live connections, whose
		// request context would otherwise only die with the socket.
		BaseContext: func(net.Listener) context.Context { return s.rootCtx },
	}
	s.srvMu.Lock()
	if s.stopped {
		s.srvMu.Unlock()
		_ = ln.Close()
		return nil
	}
	s.srv = srv
	s.srvMu.Unlock()

	// S3 sweep: the listen succeeded, so any file in this port's staging
	// dir is a leftover of a killed instance of the same port.
	if _, err := s.ensureUploadDir(); err != nil {
		log.Printf("[plugin-serve] upload staging dir refused, not sweeping: %v", err)
	} else {
		s.sweepUploads()
	}

	log.Printf("[plugin-serve] gleann-plugin-sound HTTP plugin on %s", addr)
	if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

// Shutdown stops accepting requests, lets in-flight ones finish (until ctx
// expires — then it cancels their contexts, closes the connections and
// removes their upload files), and releases the refiner helper and the
// engine before returning so a subsequent hard exit leaks no sidecar.
func (s *Server) Shutdown(ctx context.Context) error {
	s.srvMu.Lock()
	s.stopped = true
	srv := s.srv
	s.srvMu.Unlock()
	// At the drain deadline (the ctx's deadline) revoke the root context of
	// every in-flight request: handlers parked in a hung engine and the
	// ffmpeg children they started die here, not when the host SIGKILLs us.
	stopWatch := make(chan struct{})
	defer close(stopWatch)
	go func() {
		select {
		case <-ctx.Done():
			s.rootCancel()
		case <-stopWatch:
		}
	}()
	var errs []error
	if srv != nil {
		if err := srv.Shutdown(ctx); err != nil {
			_ = srv.Close()
			errs = append(errs, fmt.Errorf("drain: %w", err))
		}
	}
	s.rootCancel()
	s.uploads.drop() // uploads of handlers that could not unwind in time
	defer s.removeFallback()
	errs = append(errs, s.release())
	return errors.Join(errs...)
}

// closeGrace bounds how long Shutdown/Close wait for refiner and engine
// Close calls to unwind. A well-behaved engine returns the moment the root
// context is revoked (its ffmpeg children were started with
// exec.CommandContext and die with it). A wedged engine would otherwise
// deadlock the shutdown against the s.mu its parked handler still holds —
// and abandoning the wait leaks nothing that outlives the process: the
// native sidecar loops on stdin and exits when this process closes the
// pipe by exiting. A variable so tests can shorten it.
var closeGrace = 800 * time.Millisecond

// BoundedClose runs fn and waits at most closeGrace for it. A Close wedged on
// an unfinished transcription is abandoned (nil is returned and logged): it
// leaks nothing that outlives the process. Used by release and by callers that
// close a refiner themselves (plugin-serve's deferred Close).
func BoundedClose(name string, fn func() error) error {
	done := make(chan error, 1)
	go func() { done <- fn() }()
	select {
	case err := <-done:
		return err
	case <-time.After(closeGrace):
		log.Printf("[plugin-serve] %s Close still blocked by an unfinished transcription; abandoning the wait", name)
		return nil
	}
}

// release closes the refiner helper and the engine, waiting at most
// closeGrace for an unfinished transcription to unwind. Once a release was
// abandoned, later calls return at once instead of waiting again.
func (s *Server) release() error {
	s.relMu.Lock()
	if s.relAbandoned {
		s.relMu.Unlock()
		return nil
	}
	if s.relDone == nil {
		s.relDone = make(chan error, 1)
		done := s.relDone
		go func() { done <- errors.Join(s.closeRefiner(), s.closeEngine()) }()
	}
	done := s.relDone
	s.relMu.Unlock()
	select {
	case err := <-done:
		done <- err // let other waiters see the result too
		return err
	case <-time.After(closeGrace):
		s.relMu.Lock()
		s.relAbandoned = true
		s.relMu.Unlock()
		log.Println("[plugin-serve] refiner/engine Close still blocked by an unfinished transcription; abandoning the wait (the sidecar exits with this process)")
		return nil
	}
}

// Close stops the listener immediately and releases the refiner helper and
// the engine. Idempotent.
func (s *Server) Close() error {
	s.srvMu.Lock()
	s.stopped = true
	srv := s.srv
	s.srvMu.Unlock()
	if srv != nil {
		_ = srv.Close()
	}
	s.rootCancel()
	s.uploads.drop()
	defer s.removeFallback()
	return s.release()
}

func (s *Server) closeEngine() error {
	s.closeEng.Do(func() {
		s.mu.Lock()
		defer s.mu.Unlock()
		if s.engine != nil {
			s.engErr = s.engine.Close()
		}
		s.modelLoaded.Store(false)
	})
	return s.engErr
}

// closeRefiner releases the /live refinement sidecar (S3). plugin_serve.go's
// defer r.Close() is a backstop; this is what runs on the Shutdown path so a
// hard exit cannot leave a native helper behind.
func (s *Server) closeRefiner() error {
	s.closeRef.Do(func() {
		if s.refiner != nil {
			s.refErr = s.refiner.Close()
		}
	})
	return s.refErr
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
	s.modelLoaded.Store(true)
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
		// S4: seconds a single /convert may take. gleann's PluginManager
		// keeps its 120 s ProcessStructured timeout unless the plugin
		// advertises a longer one, while this server allows 30 minutes.
		"timeout": int(requestTimeout / time.Second),
		// Honest dependencies: ffmpeg/ffprobe presence and version (probed
		// at most once per depProbeTTL so /health stays fast).
		"deps": s.dependencies(r.Context()),
		// "off" (none configured), "up" (helper alive), "restarting" (died,
		// the supervisor is bringing it back) or "down" (given up).
		"refiner": s.refinerState(),
		"model":   map[string]bool{"loaded": s.modelLoaded.Load()},
	})
}

// refinerState reports the refinement sidecar for /health without touching
// the helper's mutex, so a busy Refine call cannot stall /health.
func (s *Server) refinerState() string {
	if s.refiner == nil {
		return "off"
	}
	if h, ok := s.refiner.(core.RefinerHealth); ok {
		return h.Status()
	}
	if a, ok := s.refiner.(interface{ Alive() bool }); ok && !a.Alive() {
		return "down"
	}
	return "up"
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
	if !s.authorized(r) {
		writeError(w, http.StatusUnauthorized, "invalid or missing token")
		return
	}

	// S4 honest fail-fast: if the (cached) dependency probe says ffmpeg is
	// absent, answer 503 with an install hint instead of accepting an
	// upload that every later step would fail on.
	if !s.dependencies(r.Context())["ffmpeg"].OK {
		writeDependencyMissing(w)
		return
	}

	// The 256 MB argument of ParseMultipartForm is only the in-memory
	// threshold; the body itself is capped so an oversized upload is refused
	// instead of being spooled to disk and decoded (P-05).
	r.Body = http.MaxBytesReader(w, r.Body, maxUploadBytes)
	if err := r.ParseMultipartForm(32 << 20); err != nil {
		var tooBig *http.MaxBytesError
		if errors.As(err, &tooBig) {
			writeError(w, http.StatusRequestEntityTooLarge, fmt.Sprintf("upload exceeds %d MB", maxUploadBytes>>20))
			return
		}
		writeError(w, http.StatusBadRequest, fmt.Sprintf("parse multipart: %v", err))
		return
	}

	file, header, err := r.FormFile("file")
	if err != nil {
		writeError(w, http.StatusBadRequest, fmt.Sprintf("missing 'file' field: %v", err))
		return
	}
	defer file.Close()

	// Save to a staging file under TempDir/gleann-plugin-sound/port-N/,
	// preserving the extension (ffmpeg needs it for format detection). The
	// per-port dir lets the startup sweep clean up after a SIGKILLed
	// instance of this port without touching other ports (S3).
	ext := strings.ToLower(filepath.Ext(header.Filename))
	if !extSupported(ext) {
		writeError(w, http.StatusUnsupportedMediaType, fmt.Sprintf("unsupported file extension %q (supported: %s)", ext, strings.Join(SupportedExts, " ")))
		return
	}
	dir, err := s.stagingDir()
	if err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Sprintf("create upload staging dir: %v", err))
		return
	}
	tmp, err := os.CreateTemp(dir, "upload-*"+ext)
	if err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Sprintf("create temp file: %v", err))
		return
	}
	tmpPath := tmp.Name()
	s.uploads.add(tmpPath)
	defer func() {
		_ = os.Remove(tmpPath)
		s.uploads.remove(tmpPath)
	}()

	if _, err := io.Copy(tmp, file); err != nil {
		tmp.Close()
		writeError(w, http.StatusInternalServerError, fmt.Sprintf("write temp file: %v", err))
		return
	}
	tmp.Close()

	ctx, cancel := context.WithTimeout(r.Context(), requestTimeout)
	defer cancel()
	// Backstop for the declared-duration check: decoders stop at the cap.
	ctx = core.WithMaxDecode(ctx, s.maxDuration)

	if err := validateUpload(ctx, tmpPath); err != nil {
		if st := decodeStatus(err); st == http.StatusServiceUnavailable {
			writeDependencyMissing(w)
		} else {
			writeError(w, st, err.Error())
		}
		return
	}
	if status, msg := s.checkDuration(ctx, tmpPath); status != 0 {
		writeError(w, status, msg)
		return
	}

	var segments []core.Segment
	if s.refiner != nil {
		// A refiner that has given up restarting would refuse every piece:
		// say so before spending CPU on the decode.
		if s.refinerDownNow(w) {
			return
		}
		// Offline path: Silero finds the speech, speaker turns are split and
		// the accurate model transcribes each piece (no streaming model needed).
		pcm, err := core.DecodeFile(ctx, tmpPath)
		if err != nil {
			if errors.Is(err, core.ErrTooLong) {
				s.writeTooLong(w)
				return
			}
			if decodeStatus(err) == http.StatusServiceUnavailable {
				writeDependencyMissing(w)
				return
			}
			writeError(w, decodeStatus(err), fmt.Sprintf("decode: %v", err))
			return
		}
		vad, closeVAD := audio.OpenVAD()
		segments, err = pipeline.TranscribeFile(ctx, pcm, vad, s.refiner,
			pipeline.FileConfig{Corrector: s.corrector, Speakers: s.speakers})
		closeVAD()
		if err != nil {
			// The supervision refusing work is the plugin's state, not the
			// upload's fault: 503 + Retry-After, never a 500.
			if s.refinerRefused(w, err) {
				return
			}
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
			if errors.Is(err, core.ErrTooLong) {
				s.writeTooLong(w)
				return
			}
			// A supervised engine whose helper died: temporary, not a 500.
			if s.refinerRefused(w, err) {
				return
			}
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
