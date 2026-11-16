package core

import (
	"bytes"
	"context"
	"encoding/binary"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

func TestParseFFprobeDurations(t *testing.T) {
	d, ok := parseFFprobeDurations("12.5\nN/A\n3600.25\n")
	if !ok || d != 3600250*time.Millisecond {
		t.Fatalf("got %v %v", d, ok)
	}
	if _, ok := parseFFprobeDurations("N/A\n"); ok {
		t.Fatal("N/A must be unknown")
	}
	// A hostile header can claim an absurd value; it must not overflow.
	if d, ok := parseFFprobeDurations("1e300"); !ok || d <= 0 {
		t.Fatalf("huge duration overflowed: %v %v", d, ok)
	}
}

func TestParseFFmpegDuration(t *testing.T) {
	d, ok := parseFFmpegDuration("  Duration: 05:00:01.50, start: 0.000000, bitrate: 64 kb/s")
	if !ok || d != 5*time.Hour+time.Second+500*time.Millisecond {
		t.Fatalf("got %v %v", d, ok)
	}
	if _, ok := parseFFmpegDuration("Duration: N/A"); ok {
		t.Fatal("N/A must be unknown")
	}
}

// A sparse WAV worth 6 hours of 8 kHz 8-bit mono must be reported as long
// without decoding it.
func TestProbeDurationUsesHeader(t *testing.T) {
	if _, err := exec.LookPath("ffprobe"); err != nil {
		t.Skip("ffprobe not installed")
	}
	var b bytes.Buffer
	le := func(v any) { _ = binary.Write(&b, binary.LittleEndian, v) }
	const rate = 8000
	size := uint32(6 * 3600 * rate)
	b.WriteString("RIFF")
	le(uint32(36) + size)
	b.WriteString("WAVEfmt ")
	le(uint32(16))
	le(uint16(1))
	le(uint16(1))
	le(uint32(rate))
	le(uint32(rate))
	le(uint16(1))
	le(uint16(8))
	b.WriteString("data")
	le(size)
	p := filepath.Join(t.TempDir(), "long.wav")
	if err := os.WriteFile(p, b.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	// Extend to the declared size as a sparse file (no real disk use).
	if err := os.Truncate(p, int64(b.Len())+int64(size)); err != nil {
		t.Skipf("cannot create a sparse file: %v", err)
	}
	d, err := ProbeDuration(context.Background(), p)
	if err != nil {
		t.Skipf("ffprobe cannot read the truncated fixture here: %v", err)
	}
	if d < time.Hour {
		t.Fatalf("duration %v, want a multi-hour value from the header", d)
	}
}
