package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/tevfik/gleann-plugin-sound/internal/core"
)

type nopStream struct{ closed bool }

func (s *nopStream) Accept(context.Context, []int16) (core.LiveUpdate, error) {
	return core.LiveUpdate{}, nil
}
func (s *nopStream) Flush(context.Context) (core.LiveUpdate, error) { return core.LiveUpdate{}, nil }
func (s *nopStream) Reset()                                         {}
func (s *nopStream) Close() error                                   { s.closed = true; return nil }

type nopEngine struct{ opened int }

func (e *nopEngine) NewLiveStream() (core.LiveStream, error) { e.opened++; return &nopStream{}, nil }
func (e *nopEngine) TranscribeStream(context.Context, []int16) (string, error) {
	return "", nil
}
func (e *nopEngine) TranscribeStreamSegments(context.Context, []int16) ([]core.Segment, error) {
	return nil, nil
}
func (e *nopEngine) TranscribeFile(context.Context, string) ([]core.Segment, error) { return nil, nil }
func (e *nopEngine) SetLanguage(string)                                             {}
func (e *nopEngine) Close() error                                                   { return nil }

func TestRecoverStream(t *testing.T) {
	eng := &nopEngine{}
	old := &nopStream{}

	if got := recoverStream(eng, old, nil); got != core.LiveStream(old) || eng.opened != 0 {
		t.Error("a healthy stream must be kept")
	}
	if got := recoverStream(eng, old, context.Canceled); got != core.LiveStream(old) || eng.opened != 0 {
		t.Error("a normal shutdown must not restart the stream")
	}
	fresh := recoverStream(eng, old, errors.New("asr helper died: EOF"))
	if fresh == core.LiveStream(old) || eng.opened != 1 || !old.closed {
		t.Errorf("a failed stream must be closed and replaced (opened=%d closed=%v)", eng.opened, old.closed)
	}
}

func TestResolveRefineModel(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	if err := os.MkdirAll(filepath.Join(home, ".gleann"), 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := `{"refine_model":"/models/qwen.gguf","completed":true}`
	if err := os.WriteFile(filepath.Join(home, ".gleann", "sound.json"), []byte(cfg), 0o644); err != nil {
		t.Fatal(err)
	}
	for in, want := range map[string]string{"": "/models/qwen.gguf", "off": "", "NONE": "", "/other.gguf": "/other.gguf"} {
		if got := resolveRefineModel(in); got != want {
			t.Errorf("resolveRefineModel(%q) = %q, want %q", in, got, want)
		}
	}
}
