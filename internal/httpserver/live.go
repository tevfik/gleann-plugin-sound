package httpserver

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"

	"github.com/tevfik/gleann-plugin-sound/internal/core"
	"github.com/tevfik/gleann-plugin-sound/internal/pipeline"
)

// DefaultMaxLive is the number of concurrent /live sessions.
const DefaultMaxLive = 4

// Live protocol (WebSocket, GET /live):
//
//	query:   lang=tr        language code, default: server language, "auto" detects
//	         token=...      access token (or "Authorization: Bearer ...")
//	         partials=0     suppress partial events
//	client → server
//	         binary frames  raw PCM: signed 16-bit little-endian, mono, 16000 Hz
//	         {"type":"end"} no more audio; the last words are flushed
//	server → client (JSON text frames)
//	         {"type":"ready","sample_rate":16000,"format":"pcm_s16le"}
//	         {"type":"partial","seq":0,"text":"...","start":1.2,"end":3.4}
//	         {"type":"final","seq":0,"text":"...","start":1.2,"end":5.0,"speaker":"Ayşe"}
//	                "speaker" is present when the server identifies voices
//	                ("?" = matches nobody enrolled)
//	         {"type":"refined","seq":0,"text":"..."}  later, if the server has a
//	                refiner model: a more accurate transcript of utterance seq
//	                (several messages with "part"/"parts" when speakers took turns)
//	         {"type":"done"}  after "end" or a normal close
//	         {"type":"error","message":"..."}
//
// Times are seconds on the session's audio clock. A "partial" carries the
// utterance so far (finalised words plus the word still being formed) and
// supersedes the previous partial; a "final" ends the utterance.
const liveSampleRate = 16000

var upgrader = websocket.Upgrader{
	ReadBufferSize:  16 << 10,
	WriteBufferSize: 16 << 10,
	// Native apps send no Origin; browsers are covered by the token.
	CheckOrigin: func(*http.Request) bool { return true },
}

type liveMsg struct {
	Type       string  `json:"type"`
	Seq        int     `json:"seq"`
	Text       string  `json:"text,omitempty"`
	Start      float64 `json:"start,omitempty"`
	End        float64 `json:"end,omitempty"`
	Speaker    string  `json:"speaker,omitempty"`
	Part       int     `json:"part,omitempty"`
	Parts      int     `json:"parts,omitempty"`
	Message    string  `json:"message,omitempty"`
	SampleRate int     `json:"sample_rate,omitempty"`
	Format     string  `json:"format,omitempty"`
}

func isLoopback(host string) bool {
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func (s *Server) authorized(r *http.Request) bool {
	if s.token == "" {
		return true
	}
	got := r.URL.Query().Get("token")
	if h := r.Header.Get("Authorization"); strings.HasPrefix(h, "Bearer ") {
		got = strings.TrimPrefix(h, "Bearer ")
	}
	return subtle.ConstantTimeCompare([]byte(got), []byte(s.token)) == 1
}

func (s *Server) handleLive(w http.ResponseWriter, r *http.Request) {
	if !s.authorized(r) {
		writeError(w, http.StatusUnauthorized, "invalid or missing token")
		return
	}
	select {
	case s.liveSlot <- struct{}{}:
		defer func() { <-s.liveSlot }()
	default:
		writeError(w, http.StatusServiceUnavailable, "too many live sessions")
		return
	}

	engine, err := s.getEngine()
	if err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Sprintf("load model: %v", err))
		return
	}
	lang := s.language
	if q, ok := r.URL.Query()["lang"]; ok && len(q) > 0 {
		lang = q[0]
	}
	var stream core.LiveStream
	switch e := engine.(type) {
	case core.LanguageLiveTranscriber:
		stream, err = e.NewLiveStreamLang(lang)
	case core.LiveTranscriber:
		stream, err = e.NewLiveStream()
	default:
		writeError(w, http.StatusNotImplemented, "the configured backend has no native streaming; use --backend nemotron")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	defer stream.Close()

	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		return // Upgrade already replied
	}
	defer conn.Close()
	partials := r.URL.Query().Get("partials") != "0"
	s.serveLive(r.Context(), conn, stream, partials)
}

func (s *Server) serveLive(parent context.Context, conn *websocket.Conn, stream core.LiveStream, partials bool) {
	ctx, cancel := context.WithCancel(parent)
	defer cancel()

	var wmu sync.Mutex
	send := func(m liveMsg) error {
		wmu.Lock()
		defer wmu.Unlock()
		_ = conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
		return conn.WriteJSON(m)
	}

	const idle = 60 * time.Second
	conn.SetReadLimit(1 << 20)
	_ = conn.SetReadDeadline(time.Now().Add(idle))
	conn.SetPongHandler(func(string) error { return conn.SetReadDeadline(time.Now().Add(idle)) })

	// Keep-alive pings; mobile networks drop silent connections.
	go func() {
		t := time.NewTicker(20 * time.Second)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				wmu.Lock()
				err := conn.WriteControl(websocket.PingMessage, nil, time.Now().Add(5*time.Second))
				wmu.Unlock()
				if err != nil {
					cancel()
					return
				}
			}
		}
	}()

	if err := send(liveMsg{Type: "ready", SampleRate: liveSampleRate, Format: "pcm_s16le"}); err != nil {
		return
	}

	// Reader: socket → audio channel. A blocking send is the backpressure
	// that makes a client uploading faster than real time simply wait.
	audioCh := make(chan []int16, 64)
	go func() {
		defer close(audioCh)
		for {
			typ, data, err := conn.ReadMessage()
			if err != nil {
				return
			}
			_ = conn.SetReadDeadline(time.Now().Add(idle))
			switch typ {
			case websocket.BinaryMessage:
				if len(data) < 2 {
					continue
				}
				pcm := make([]int16, len(data)/2)
				for i := range pcm {
					pcm[i] = int16(uint16(data[2*i]) | uint16(data[2*i+1])<<8)
				}
				select {
				case audioCh <- pcm:
				case <-ctx.Done():
					return
				}
			case websocket.TextMessage:
				var m liveMsg
				if json.Unmarshal(data, &m) == nil && m.Type == "end" {
					return
				}
			}
		}
	}()

	vad, closeVAD := s.openVAD()
	defer closeVAD()
	cfg := pipeline.DefaultLiveConfig()
	cfg.Corrector = s.corrector
	cfg.Refiner = s.refiner
	cfg.Speakers = s.speakers
	err := pipeline.RunLive(ctx, stream, vad, audioCh, cfg, func(ev pipeline.LiveEvent) {
		m := liveMsg{Seq: ev.Seq, Start: ev.Start.Seconds(), End: ev.End.Seconds(), Speaker: ev.Speaker}
		if ev.Refined {
			m.Type, m.Text, m.Part, m.Parts = "refined", ev.Text, ev.Part, ev.Parts
		} else if ev.Final {
			m.Type, m.Text = "final", ev.Text
		} else {
			if !partials {
				return
			}
			m.Type, m.Text = "partial", strings.TrimSpace(ev.Text+" "+ev.Pending)
		}
		if send(m) != nil {
			cancel()
		}
	})
	if err != nil && ctx.Err() == nil {
		log.Printf("[live] session error: %v", err)
		_ = send(liveMsg{Type: "error", Message: err.Error()})
	}
	_ = send(liveMsg{Type: "done"})
	wmu.Lock()
	_ = conn.WriteControl(websocket.CloseMessage,
		websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""), time.Now().Add(2*time.Second))
	wmu.Unlock()
}
