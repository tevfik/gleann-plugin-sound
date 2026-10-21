package tcasr

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/tevfik/gleann-plugin-sound/internal/core"
)

func models(t *testing.T) (nemotron, qwen string) {
	t.Helper()
	if testing.Short() {
		t.Skip("model-backed test skipped with -short")
	}
	if _, err := FindHelper(); err != nil {
		t.Skip(err)
	}
	home, _ := os.UserHomeDir()
	dir := filepath.Join(home, ".gleann", "models")
	nemotron = filepath.Join(dir, "nemotron-3.5-asr-streaming-0.6b-Q8_0.gguf")
	qwen = filepath.Join(dir, "Qwen3-ASR-1.7B-Q8_0.gguf")
	for _, f := range []string{nemotron, qwen} {
		if _, err := os.Stat(f); err != nil {
			t.Skipf("model missing: %s", f)
		}
	}
	return
}

func jfk(t *testing.T) []int16 {
	t.Helper()
	pcm, err := core.DecodeFile(context.Background(), filepath.Join("..", "..", "test", "testdata", "jfk.wav"))
	if err != nil {
		t.Skipf("cannot decode test audio: %v", err)
	}
	return pcm
}

func norm(s string) string {
	return strings.ToLower(strings.Join(strings.Fields(strings.NewReplacer(".", "", ",", "").Replace(s)), " "))
}

func TestLocale(t *testing.T) {
	for in, want := range map[string]string{"tr": "tr-TR", "TR": "tr-TR", "en-GB": "en-GB", "": "auto", "auto": "auto", "xx": "xx"} {
		if got := Locale(in); got != want {
			t.Errorf("Locale(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestLiveStreamIsIncrementalAndWordSafe(t *testing.T) {
	nem, _ := models(t)
	e, err := NewEngine(nem)
	if err != nil {
		t.Fatal(err)
	}
	defer e.Close()
	st, err := e.NewLiveStreamLang("en")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	pcm := jfk(t)
	ctx := context.Background()
	var text strings.Builder
	var first time.Duration
	for i := 0; i < len(pcm); i += 480 {
		u, err := st.Accept(ctx, pcm[i:min(i+480, len(pcm))])
		if err != nil {
			t.Fatal(err)
		}
		if u.Delta != "" && first == 0 {
			first = u.Audio
		}
		text.WriteString(u.Delta)
	}
	u, err := st.Flush(ctx)
	if err != nil {
		t.Fatal(err)
	}
	text.WriteString(u.Delta)

	got := norm(text.String())
	t.Logf("first word after %v of audio: %q", first, got)
	if !strings.Contains(got, "ask not what your country can do for you") {
		t.Errorf("transcript %q", got)
	}
	if first == 0 || first > 3*time.Second {
		t.Errorf("first word only after %v of audio", first)
	}

	// The stream is reusable after a flush.
	u, err = st.Accept(ctx, pcm[:16000*4])
	if err != nil {
		t.Fatal(err)
	}
	_ = u
}

func TestRefinerIsAccurate(t *testing.T) {
	_, qwen := models(t)
	r, err := NewRefiner(qwen, "en")
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	start := time.Now()
	text, err := r.Refine(context.Background(), jfk(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("refined in %v: %q", time.Since(start).Round(10*time.Millisecond), text)
	if got := norm(text); !strings.Contains(got, "ask not what your country can do for you") {
		t.Errorf("refined transcript %q", got)
	}
}
