package httpserver

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
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
	fw.Write([]byte("not really audio; the fake engine ignores it"))
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
		json.NewDecoder(resp.Body).Decode(&got)
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
	json.NewDecoder(resp.Body).Decode(&got)
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
