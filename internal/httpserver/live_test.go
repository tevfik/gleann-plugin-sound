package httpserver

import (
	"context"
	"encoding/binary"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"github.com/tevfik/gleann-plugin-sound/internal/core"
)

// liveEngine recognises one word per 0.5 s of audio and records the language
// each stream was opened with.
type liveEngine struct {
	fakeEngine
	langs chan string
}

func (l *liveEngine) NewLiveStream() (core.LiveStream, error) { return &wordStream{}, nil }
func (l *liveEngine) NewLiveStreamLang(lang string) (core.LiveStream, error) {
	l.langs <- lang
	return &wordStream{}, nil
}

type wordStream struct {
	fed, words int
	held       bool
}

func (w *wordStream) Accept(_ context.Context, pcm []int16) (core.LiveUpdate, error) {
	before := w.fed / 8000
	w.fed += len(pcm)
	var u core.LiveUpdate
	for i := before; i < w.fed/8000; i++ {
		if w.held {
			u.Delta += fmt.Sprintf(" w%d", w.words)
		}
		w.words++
		w.held = true
	}
	return u, nil
}
func (w *wordStream) Flush(context.Context) (core.LiveUpdate, error) {
	if w.held {
		w.held = false
		return core.LiveUpdate{Delta: fmt.Sprintf(" w%d", w.words)}, nil
	}
	return core.LiveUpdate{}, nil
}
func (w *wordStream) Reset()       { *w = wordStream{} }
func (w *wordStream) Close() error { return nil }

func liveServer(t *testing.T, token string, max int) (*httptest.Server, *liveEngine) {
	t.Helper()
	eng := &liveEngine{langs: make(chan string, 8)}
	s := New(func() (core.Transcriber, error) { return eng, nil }, "tr", 0)
	if err := s.SetListen("127.0.0.1", token, max); err != nil {
		t.Fatal(err)
	}
	// Deterministic detector: anything loud is speech (the real one is Silero).
	s.SetVADFactory(func() (core.VADProvider, func()) { return loudVAD{}, func() {} })
	ts := httptest.NewServer(s.Handler())
	t.Cleanup(ts.Close)
	return ts, eng
}

func dial(t *testing.T, ts *httptest.Server, query string) (*websocket.Conn, *http.Response, error) {
	t.Helper()
	url := "ws" + strings.TrimPrefix(ts.URL, "http") + "/live" + query
	return websocket.DefaultDialer.Dial(url, nil)
}

func tonePCM(ms int) []byte {
	b := make([]byte, ms*16*2)
	for i := 0; i < len(b); i += 2 {
		binary.LittleEndian.PutUint16(b[i:], uint16(int16(5000)))
	}
	return b
}

func TestLiveSession(t *testing.T) {
	ts, eng := liveServer(t, "", 2)
	c, _, err := dial(t, ts, "?lang=en")
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	var ready liveMsg
	if err := c.ReadJSON(&ready); err != nil || ready.Type != "ready" || ready.SampleRate != 16000 {
		t.Fatalf("ready: %+v %v", ready, err)
	}
	if got := <-eng.langs; got != "en" {
		t.Errorf("stream opened with language %q, want en", got)
	}

	// 2 s of speech, then 1.2 s of silence ends the utterance, then "end".
	for i := 0; i < 2000; i += 100 {
		_ = c.WriteMessage(websocket.BinaryMessage, tonePCM(100))
	}
	for i := 0; i < 1200; i += 100 {
		_ = c.WriteMessage(websocket.BinaryMessage, make([]byte, 100*16*2))
	}
	_ = c.WriteJSON(liveMsg{Type: "end"})

	var partials, finals int
	var final liveMsg
	_ = c.SetReadDeadline(time.Now().Add(5 * time.Second))
	for {
		var m liveMsg
		if err := c.ReadJSON(&m); err != nil {
			t.Fatalf("read: %v (partials=%d finals=%d)", err, partials, finals)
		}
		if m.Type == "partial" {
			partials++
		}
		if m.Type == "final" {
			finals++
			final = m
		}
		if m.Type == "done" {
			break
		}
	}
	if partials == 0 || finals != 1 {
		t.Fatalf("partials=%d finals=%d", partials, finals)
	}
	if len(strings.Fields(final.Text)) < 4 || final.End <= final.Start {
		t.Errorf("final = %+v", final)
	}
}

func TestLiveAuth(t *testing.T) {
	ts, _ := liveServer(t, "s3cret", 2)
	if _, resp, err := dial(t, ts, ""); err == nil || resp == nil || resp.StatusCode != 401 {
		t.Fatalf("missing token must be rejected with 401, got %v %v", resp, err)
	}
	if _, resp, err := dial(t, ts, "?token=wrong"); err == nil || resp.StatusCode != 401 {
		t.Fatalf("wrong token must be rejected")
	}
	c, _, err := dial(t, ts, "?token=s3cret")
	if err != nil {
		t.Fatalf("valid token rejected: %v", err)
	}
	c.Close()
}

func TestLiveCapacity(t *testing.T) {
	ts, _ := liveServer(t, "", 1)
	c1, _, err := dial(t, ts, "")
	if err != nil {
		t.Fatal(err)
	}
	defer c1.Close()
	var m liveMsg
	_ = c1.ReadJSON(&m) // session 1 is established and holds the slot
	if _, resp, err := dial(t, ts, ""); err == nil || resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("second session must get 503, got %v %v", resp, err)
	}
}

func TestListenOnNetworkRequiresToken(t *testing.T) {
	s := New(nil, "", 0)
	if err := s.SetListen("0.0.0.0", "", 0); err == nil {
		t.Error("0.0.0.0 without a token must be refused")
	}
	if err := s.SetListen("0.0.0.0", "x", 0); err != nil {
		t.Errorf("with a token it must be allowed: %v", err)
	}
	if err := s.SetListen("", "", 0); err != nil {
		t.Errorf("default loopback needs no token: %v", err)
	}
}

func TestLiveNeedsStreamingBackend(t *testing.T) {
	ts := newServer() // plain fakeEngine: no streaming
	defer ts.Close()
	_, resp, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(ts.URL, "http")+"/live", nil)
	if err == nil || resp == nil || resp.StatusCode != http.StatusNotImplemented {
		t.Fatalf("want 501, got %v %v", resp, err)
	}
}

type loudVAD struct{}

func (loudVAD) IsSpeech(p []int16) bool {
	for _, v := range p {
		if v > 1000 || v < -1000 {
			return true
		}
	}
	return false
}
func (loudVAD) Reset() {}
