package core

import (
	"context"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// Core types tests
// ---------------------------------------------------------------------------

func TestSegment_Fields(t *testing.T) {
	seg := Segment{
		Start: 1 * time.Second,
		End:   2 * time.Second,
		Text:  "hello world",
	}

	if seg.Start != time.Second {
		t.Errorf("Start: want 1s, got %v", seg.Start)
	}
	if seg.End != 2*time.Second {
		t.Errorf("End: want 2s, got %v", seg.End)
	}
	if seg.Text != "hello world" {
		t.Errorf("Text: want %q, got %q", "hello world", seg.Text)
	}
}

func TestTranscriptionEvent_Fields(t *testing.T) {
	evt := TranscriptionEvent{
		Segments: []Segment{
			{Start: 0, End: time.Second, Text: "test"},
		},
		Final: true,
	}

	if len(evt.Segments) != 1 {
		t.Fatalf("expected 1 segment, got %d", len(evt.Segments))
	}
	if !evt.Final {
		t.Error("expected Final to be true")
	}
}

func TestTranscriptionHandler_Callable(t *testing.T) {
	var called bool
	handler := TranscriptionHandler(func(event TranscriptionEvent) {
		called = true
		if len(event.Segments) != 1 {
			t.Errorf("expected 1 segment, got %d", len(event.Segments))
		}
	})

	handler(TranscriptionEvent{
		Segments: []Segment{{Text: "test"}},
	})

	if !called {
		t.Error("handler was not called")
	}
}

// ---------------------------------------------------------------------------
// Interface compliance tests using mock implementations
// ---------------------------------------------------------------------------

// mockTranscriber is a test double for core.Transcriber.
type mockTranscriber struct {
	closed        bool
	lastPCMLen    int
	lastFilePath  string
	returnText    string
	returnSegments []Segment
	returnErr     error
}

func (m *mockTranscriber) TranscribeStream(_ context.Context, pcmData []int16) (string, error) {
	m.lastPCMLen = len(pcmData)
	return m.returnText, m.returnErr
}

func (m *mockTranscriber) TranscribeStreamSegments(_ context.Context, pcmData []int16) ([]Segment, error) {
	m.lastPCMLen = len(pcmData)
	return m.returnSegments, m.returnErr
}

func (m *mockTranscriber) TranscribeFile(_ context.Context, filepath string) ([]Segment, error) {
	m.lastFilePath = filepath
	return m.returnSegments, m.returnErr
}

func (m *mockTranscriber) Close() error {
	m.closed = true
	return m.returnErr
}

func (m *mockTranscriber) SetLanguage(_ string) {}

var _ Transcriber = (*mockTranscriber)(nil)

// mockAudioCapturer is a test double for core.AudioCapturer.
type mockAudioCapturer struct {
	started bool
	stopped bool
}

func (m *mockAudioCapturer) Start(_ context.Context, onData func(pcmData []int16)) error {
	m.started = true
	// Simulate one callback.
	onData([]int16{100, 200, 300})
	return nil
}

func (m *mockAudioCapturer) Stop() error {
	m.stopped = true
	return nil
}

var _ AudioCapturer = (*mockAudioCapturer)(nil)

// mockKeyboardInjector is a test double for core.KeyboardInjector.
type mockKeyboardInjector struct {
	lastText string
}

func (m *mockKeyboardInjector) TypeText(text string) error {
	m.lastText = text
	return nil
}

var _ KeyboardInjector = (*mockKeyboardInjector)(nil)

func TestMockTranscriber(t *testing.T) {
	m := &mockTranscriber{
		returnText: "hello",
		returnSegments: []Segment{
			{Text: "hello", Start: 0, End: time.Second},
		},
	}

	text, err := m.TranscribeStream(context.Background(), make([]int16, 16000))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if text != "hello" {
		t.Errorf("want %q, got %q", "hello", text)
	}
	if m.lastPCMLen != 16000 {
		t.Errorf("lastPCMLen: want 16000, got %d", m.lastPCMLen)
	}

	segs, err := m.TranscribeStreamSegments(context.Background(), make([]int16, 8000))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(segs) != 1 || segs[0].Text != "hello" {
		t.Errorf("unexpected segments: %v", segs)
	}

	segs, err = m.TranscribeFile(context.Background(), "/path/to/file.wav")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if m.lastFilePath != "/path/to/file.wav" {
		t.Errorf("lastFilePath: want %q, got %q", "/path/to/file.wav", m.lastFilePath)
	}

	m.Close()
	if !m.closed {
		t.Error("expected closed to be true")
	}
}

func TestMockAudioCapturer(t *testing.T) {
	m := &mockAudioCapturer{}
	var received []int16

	err := m.Start(context.Background(), func(pcm []int16) {
		received = append(received, pcm...)
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !m.started {
		t.Error("expected started to be true")
	}
	if len(received) != 3 {
		t.Errorf("expected 3 samples, got %d", len(received))
	}

	m.Stop()
	if !m.stopped {
		t.Error("expected stopped to be true")
	}
}

func TestMockKeyboardInjector(t *testing.T) {
	m := &mockKeyboardInjector{}
	err := m.TypeText("hello world")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if m.lastText != "hello world" {
		t.Errorf("lastText: want %q, got %q", "hello world", m.lastText)
	}
}
