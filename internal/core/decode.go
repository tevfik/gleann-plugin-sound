package core

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"time"
)

// ErrTooLong means the decoded audio is longer than the cap set with
// WithMaxDecode, whatever length the container declared.
var ErrTooLong = errors.New("decoded audio exceeds the duration limit")

type maxDecodeKey struct{}

// decodeMargin is the slack on top of the cap (container rounding, resampler
// priming) before decoded audio counts as too long.
const decodeMargin = time.Second

// WithMaxDecode returns a context that makes DecodeFile stop and return
// ErrTooLong once the decoded audio exceeds d (plus a one second margin). It
// is the backstop for the container-declared duration check, which a crafted
// file can fool. d <= 0 means no cap.
func WithMaxDecode(ctx context.Context, d time.Duration) context.Context {
	return context.WithValue(ctx, maxDecodeKey{}, d)
}

// maxDecodeBytes returns the PCM byte cap carried by ctx (16 kHz, mono, 16 bit)
// or 0 for none.
func maxDecodeBytes(ctx context.Context) int64 {
	d, _ := ctx.Value(maxDecodeKey{}).(time.Duration)
	if d <= 0 {
		return 0
	}
	const bytesPerSecond = 16000 * 1 * 2
	sec := (d + decodeMargin).Seconds()
	if sec > float64(1<<40) {
		return 0
	}
	return int64(sec * bytesPerSecond)
}

// DecodeFile decodes any audio or video file to 16 kHz mono PCM through ffmpeg.
// When ctx carries a cap (WithMaxDecode) the ffmpeg process is killed as soon
// as its output crosses it, so an over-long stream is never buffered whole.
func DecodeFile(ctx context.Context, path string) ([]int16, error) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	cmd := exec.CommandContext(ctx,
		"ffmpeg", "-v", "error", "-i", path,
		"-ar", "16000", "-ac", "1", "-f", "s16le", "-acodec", "pcm_s16le", "-",
	)
	// A child that outlives the killed ffmpeg must not hold Wait forever.
	cmd.WaitDelay = 3 * time.Second
	var stderr limitedBuffer
	cmd.Stderr = &stderr
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("ffmpeg decode failed: %w", err)
	}
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("ffmpeg decode failed: %w", err)
	}

	limit := maxDecodeBytes(ctx)
	// Samples are converted per chunk straight into pcm, so the raw bytes are
	// never held as a second whole-stream buffer.
	var pcm []int16
	var total int64
	var odd []byte // a trailing byte that waits for its pair
	chunk := make([]byte, 256<<10)
	tooLong := false
	var readErr error
	for {
		n, rerr := stdout.Read(chunk)
		total += int64(n)
		if limit > 0 && total > limit {
			tooLong = true
			cancel()
			_ = stdout.Close()
			break
		}
		buf := chunk[:n]
		if len(odd) > 0 && len(buf) > 0 {
			pcm = append(pcm, int16(uint16(odd[0])|uint16(buf[0])<<8))
			odd, buf = nil, buf[1:]
		}
		for i := 0; i+1 < len(buf); i += 2 {
			pcm = append(pcm, int16(uint16(buf[i])|uint16(buf[i+1])<<8))
		}
		if len(buf)%2 == 1 {
			odd = []byte{buf[len(buf)-1]}
		}
		if rerr != nil {
			if rerr != io.EOF {
				readErr = rerr
			}
			break
		}
	}
	waitErr := cmd.Wait()
	if tooLong {
		return nil, ErrTooLong
	}
	if readErr != nil {
		return nil, fmt.Errorf("ffmpeg decode failed: %w", readErr)
	}
	if waitErr != nil {
		if msg := strings.TrimSpace(stderr.String()); msg != "" {
			if len(msg) > 200 {
				msg = msg[:200]
			}
			return nil, fmt.Errorf("ffmpeg cannot decode this file (no audio stream, corrupt or unsupported): %s: %w", msg, waitErr)
		}
		return nil, fmt.Errorf("ffmpeg decode failed: %w", waitErr)
	}
	if pcm == nil {
		pcm = []int16{}
	}
	return pcm, nil
}

// limitedBuffer keeps the first 4 KiB written to it (ffmpeg's error text).
type limitedBuffer struct{ b []byte }

func (l *limitedBuffer) Write(p []byte) (int, error) {
	if room := 4096 - len(l.b); room > 0 {
		if len(p) < room {
			room = len(p)
		}
		l.b = append(l.b, p[:room]...)
	}
	return len(p), nil
}

func (l *limitedBuffer) String() string { return string(l.b) }

// Refiner re-transcribes a finished utterance with a slower, more accurate
// model than the one used live.
type Refiner interface {
	Refine(ctx context.Context, pcm []int16) (string, error)
	Close() error
}

// SpeakerLabeler names the speaker of a finished utterance. Name is empty when
// the audio is too short to tell; score is the match confidence (cosine).
type SpeakerLabeler interface {
	Label(pcm []int16) (name string, score float64)
}

// SpeakerTurn is a stretch of an utterance spoken by one person.
type SpeakerTurn struct {
	Start, End time.Duration // offsets inside the utterance
	Name       string
	Score      float64
}

// TurnSplitter is implemented by labelers that can find speaker changes inside
// one utterance (people answering each other without a pause). Turns returns
// nil when it cannot or need not split.
type TurnSplitter interface {
	Turns(pcm []int16) []SpeakerTurn
}

// uploadFormats are the ffmpeg demuxers accepted for uploaded media. Playlist
// style demuxers (hls, concat, sdp, ...) are deliberately absent: they can
// reference other local files and would turn /convert into a file reader.
const uploadFormats = "wav,mp3,flac,ogg,mov,matroska,webm,aac,ac3,avi,asf,flv,mpegts,mpeg,amr,aiff,caf,ape,wv,w64"

// ValidateUpload checks, without decoding, that path is a plain media file
// whose container is in uploadFormats. Run it on untrusted uploads before
// handing the file to any engine.
func ValidateUpload(ctx context.Context, path string) error {
	cmd := exec.CommandContext(ctx, "ffmpeg", "-v", "error",
		"-format_whitelist", uploadFormats, "-i", path, "-t", "0", "-f", "null", "-")
	if out, err := cmd.CombinedOutput(); err != nil {
		var ee *exec.Error
		if errors.As(err, &ee) {
			return fmt.Errorf("ffmpeg not available: %w", err)
		}
		msg := strings.TrimSpace(string(out))
		if len(msg) > 200 {
			msg = msg[:200]
		}
		return fmt.Errorf("unsupported or unsafe media container: %s", msg)
	}
	return nil
}
