package core

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

// DecodeFile decodes any audio or video file to 16 kHz mono PCM through ffmpeg.
func DecodeFile(ctx context.Context, path string) ([]int16, error) {
	cmd := exec.CommandContext(ctx,
		"ffmpeg", "-v", "error", "-i", path,
		"-ar", "16000", "-ac", "1", "-f", "s16le", "-acodec", "pcm_s16le", "-",
	)
	raw, err := cmd.Output()
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			if msg := strings.TrimSpace(string(ee.Stderr)); msg != "" {
				if len(msg) > 200 {
					msg = msg[:200]
				}
				return nil, fmt.Errorf("ffmpeg cannot decode this file (no audio stream, corrupt or unsupported): %s: %w", msg, err)
			}
		}
		return nil, fmt.Errorf("ffmpeg decode failed: %w", err)
	}
	pcm := make([]int16, len(raw)/2)
	for i := range pcm {
		pcm[i] = int16(uint16(raw[i*2]) | uint16(raw[i*2+1])<<8)
	}
	return pcm, nil
}

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
