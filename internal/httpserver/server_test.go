package httpserver

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"testing"
	"time"

	"github.com/tevfik/gleann-plugin-sound/internal/core"
)

type fakeEngine struct{ segs []core.Segment }

func (f *fakeEngine) TranscribeStream(context.Context, []int16) (string, error) { return "", nil }
func (f *fakeEngine) TranscribeStreamSegments(context.Context, []int16) ([]core.Segment, error) {
	return f.segs, nil
}
func (f *fakeEngine) TranscribeFile(context.Context, string) ([]core.Segment, error) {
	return f.segs, nil
}
func (f *fakeEngine) SetLanguage(string) {}
func (f *fakeEngine) Close() error       { return nil }

func newServerWithToken(token string, segs ...core.Segment) *httptest.Server {
	s := New(func() (core.Transcriber, error) { return &fakeEngine{segs: segs}, nil }, "tr", 0)
	s.token = token
	return httptest.NewServer(s.Handler())
}

func newServer(segs ...core.Segment) *httptest.Server {
	s := New(func() (core.Transcriber, error) { return &fakeEngine{segs: segs}, nil }, "tr", 0)
	s.SetCorrector(core.NewCorrector(map[string]string{"gılan": "gleann"}))
	return httptest.NewServer(s.Handler())
}

func upload(t *testing.T, url string) *http.Response {
	t.Helper()
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	fw, _ := mw.CreateFormFile("file", "meeting.mp3")
	_, _ = fw.Write(tinyWAV())
	mw.Close()
	resp, err := http.Post(url+"/convert", mw.FormDataContentType(), &body)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

func TestCapabilitiesMatchGleannRouting(t *testing.T) {
	ts := newServer()
	defer ts.Close()
	for _, path := range []string{"/health", "/info"} {
		resp, err := http.Get(ts.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		var got struct {
			Capabilities  []string `json:"capabilities"`
			SchemaVersion int      `json:"schema_version"`
			Version       string   `json:"version"`
		}
		_ = json.NewDecoder(resp.Body).Decode(&got)
		resp.Body.Close()
		has := map[string]bool{}
		for _, c := range got.Capabilities {
			has[c] = true
		}
		if !has["audio-transcription"] {
			t.Errorf("%s: gleann only routes audio to plugins advertising audio-transcription, got %v", path, got.Capabilities)
		}
		if got.SchemaVersion != SchemaVersion || got.Version == "" {
			t.Errorf("%s: version info missing: %+v", path, got)
		}
	}
}

func TestConvert(t *testing.T) {
	ts := newServer(
		core.Segment{Start: 0, End: 5 * time.Second, Text: "Merhaba Gılan"},
		core.Segment{Start: 7 * time.Second, End: 9 * time.Second, Text: "eklenti çalışıyor"},
	)
	defer ts.Close()
	resp := upload(t, ts.URL)
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("status %d", resp.StatusCode)
	}
	var got struct {
		Markdown string `json:"markdown"`
		Text     string `json:"text"`
		Segments []struct {
			Start, End float64
			Text       string
		} `json:"segments"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&got)
	if got.Text != "Merhaba Gleann eklenti çalışıyor" {
		t.Errorf("text = %q (corrections must apply)", got.Text)
	}
	if len(got.Segments) != 2 || got.Segments[1].Start != 7 {
		t.Errorf("segments = %+v", got.Segments)
	}
	if !bytes.Contains([]byte(got.Markdown), []byte("[00:07 - 00:09] eklenti çalışıyor")) {
		t.Errorf("markdown lacks timestamps: %q", got.Markdown)
	}
}

// gleann would index the raw JSON body of an empty transcript, so silence
// must be an error status instead.
func TestConvertNoSpeech(t *testing.T) {
	ts := newServer()
	defer ts.Close()
	resp := upload(t, ts.URL)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("status %d, want 422", resp.StatusCode)
	}
}

func TestDecodeStatus(t *testing.T) {
	if got := decodeStatus(fmt.Errorf("x: %w", exec.ErrNotFound)); got != http.StatusInternalServerError {
		t.Errorf("missing ffmpeg: status = %d, want 500", got)
	}
	if got := decodeStatus(fmt.Errorf("ffmpeg cannot decode this file")); got != http.StatusUnprocessableEntity {
		t.Errorf("bad file: status = %d, want 422", got)
	}
}

// tinyWAV returns 0.1 s of 16 kHz mono silence as a valid WAV file.
func tinyWAV() []byte {
	const n = 1600
	var b bytes.Buffer
	le := func(v any) { _ = binary.Write(&b, binary.LittleEndian, v) }
	b.WriteString("RIFF")
	le(uint32(36 + n*2))
	b.WriteString("WAVEfmt ")
	le(uint32(16))
	le(uint16(1))
	le(uint16(1))
	le(uint32(16000))
	le(uint32(32000))
	le(uint16(2))
	le(uint16(16))
	b.WriteString("data")
	le(uint32(n * 2))
	b.Write(make([]byte, n*2))
	return b.Bytes()
}

func uploadNamed(t *testing.T, url, name string, content []byte, token string) *http.Response {
	t.Helper()
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	fw, _ := mw.CreateFormFile("file", name)
	_, _ = fw.Write(content)
	mw.Close()
	req, _ := http.NewRequest("POST", url+"/convert", &body)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

// P-01: an uploaded HLS playlist must not make ffmpeg read other local files.
func TestConvertRejectsPlaylistUploads(t *testing.T) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg not installed")
	}
	ts := newServer(core.Segment{Start: 0, End: time.Second, Text: "x"})
	defer ts.Close()
	pl := []byte("#EXTM3U\n#EXT-X-TARGETDURATION:10\n#EXTINF:10.0,\nfile:///etc/hostname\n#EXT-X-ENDLIST\n")
	for _, name := range []string{"a.m3u8", "a.mp3", "a.wav"} {
		resp := uploadNamed(t, ts.URL, name, pl, "")
		resp.Body.Close()
		if resp.StatusCode < 400 {
			t.Errorf("%s: playlist upload accepted (status %d)", name, resp.StatusCode)
		}
	}
}

func TestConvertRequiresTokenWhenConfigured(t *testing.T) {
	ts := newServerWithToken("s3cret", core.Segment{Start: 0, End: time.Second, Text: "x"})
	defer ts.Close()
	if resp := uploadNamed(t, ts.URL, "a.wav", tinyWAV(), ""); resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("no token: status %d", resp.StatusCode)
	}
}

// P-02: /settings and /models refuse a request addressed to a non-loopback Host
// when the server runs without a token (DNS rebinding).
func TestSettingsRejectsForeignHost(t *testing.T) {
	h := New(func() (core.Transcriber, error) { return &fakeEngine{}, nil }, "tr", 0).Handler()
	for host, want := range map[string]bool{"127.0.0.1:8766": true, "localhost:8766": true, "evil.example.com": false} {
		req := httptest.NewRequest(http.MethodGet, "/settings", nil)
		req.Host = host
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if got := rec.Code != http.StatusUnauthorized && rec.Code != http.StatusForbidden; got != want {
			t.Errorf("Host %q: status %d, allowed=%v want %v", host, rec.Code, got, want)
		}
	}
}

// Without ffmpeg (some CI runners) the fake-engine tests cannot probe the upload;
// the playlist test below skips itself in that case.
func TestMain(m *testing.M) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		validateUpload = func(context.Context, string) error { return nil }
	}
	os.Exit(m.Run())
}
