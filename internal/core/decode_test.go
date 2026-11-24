package core

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// fakeFFmpeg puts an "ffmpeg" shell script first on PATH that writes n bytes of
// PCM to stdout, whatever the input.
func fakeFFmpeg(t *testing.T, n int) {
	t.Helper()
	dir := t.TempDir()
	script := "#!/bin/sh\nexec head -c " + itoa(n) + " /dev/zero\n"
	if err := os.WriteFile(filepath.Join(dir, "ffmpeg"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for ; n > 0; n /= 10 {
		b = append([]byte{byte('0' + n%10)}, b...)
	}
	return string(b)
}

// F1: decoded PCM beyond the cap is refused, whatever the container claimed.
func TestDecodeFileStopsAtCap(t *testing.T) {
	cap := 10 * time.Second
	fakeFFmpeg(t, 20*16000*2) // 20 s of PCM
	ctx := WithMaxDecode(context.Background(), cap)
	pcm, err := DecodeFile(ctx, "ignored.wav")
	if !errors.Is(err, ErrTooLong) {
		t.Fatalf("err = %v (len %d), want ErrTooLong", err, len(pcm))
	}
	if pcm != nil {
		t.Errorf("no PCM may be returned past the cap, got %d samples", len(pcm))
	}
}

func TestDecodeFileWithinCap(t *testing.T) {
	fakeFFmpeg(t, 10*16000*2)
	ctx := WithMaxDecode(context.Background(), 10*time.Second)
	pcm, err := DecodeFile(ctx, "ignored.wav")
	if err != nil || len(pcm) != 10*16000 {
		t.Fatalf("pcm %d err %v, want 10 s", len(pcm), err)
	}
}

func TestDecodeFileNoCapByDefault(t *testing.T) {
	fakeFFmpeg(t, 20*16000*2)
	pcm, err := DecodeFile(context.Background(), "ignored.wav")
	if err != nil || len(pcm) != 20*16000 {
		t.Fatalf("pcm %d err %v", len(pcm), err)
	}
}

// A huge stream must be stopped early, not buffered first: the process is
// killed once the cap is crossed, so this returns quickly even for 10 GB.
func TestDecodeFileStopsEarly(t *testing.T) {
	fakeFFmpeg(t, 10<<30)
	ctx := WithMaxDecode(context.Background(), time.Second)
	start := time.Now()
	_, err := DecodeFile(ctx, "ignored.wav")
	if !errors.Is(err, ErrTooLong) {
		t.Fatalf("err = %v", err)
	}
	if time.Since(start) > 10*time.Second {
		t.Fatalf("took %v: not stopped early", time.Since(start))
	}
}

// An odd byte count drops the trailing byte; samples keep their order across chunks.
func TestDecodeFileOddAndChunkBoundaries(t *testing.T) {
	fakeFFmpeg(t, 600001)
	pcm, err := DecodeFile(context.Background(), "ignored.wav")
	if err != nil || len(pcm) != 300000 {
		t.Fatalf("pcm %d err %v, want 300000 samples", len(pcm), err)
	}
}
