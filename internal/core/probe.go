package core

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// ErrDurationUnknown means the container does not declare a duration.
var ErrDurationUnknown = errors.New("media duration is not declared by the container")

// ProbeDuration reads the declared duration of a media file without decoding
// it, using ffprobe (falling back to the "Duration:" line of ffmpeg -i). It
// returns an error wrapping exec.ErrNotFound when neither tool is installed
// and ErrDurationUnknown when the container has no duration (live streams,
// some raw formats).
func ProbeDuration(ctx context.Context, path string) (time.Duration, error) {
	out, err := exec.CommandContext(ctx, "ffprobe", "-v", "error",
		"-show_entries", "format=duration:stream=duration",
		"-of", "default=noprint_wrappers=1:nokey=1", path).Output()
	if err == nil {
		if d, ok := parseFFprobeDurations(string(out)); ok {
			return d, nil
		}
		return 0, ErrDurationUnknown
	}
	var ee *exec.Error
	if !errors.As(err, &ee) {
		// ffprobe ran but failed: let the caller's own validation report it.
		return 0, fmt.Errorf("ffprobe: %w", err)
	}
	// No ffprobe: ffmpeg -i exits 1 but prints the duration on stderr.
	b, err := exec.CommandContext(ctx, "ffmpeg", "-hide_banner", "-i", path).CombinedOutput()
	var ee2 *exec.Error
	if errors.As(err, &ee2) {
		return 0, fmt.Errorf("probing duration: %w", err)
	}
	if d, ok := parseFFmpegDuration(string(b)); ok {
		return d, nil
	}
	return 0, ErrDurationUnknown
}

// parseFFprobeDurations returns the largest duration (seconds, one per line)
// printed by ffprobe; "N/A" lines are ignored.
func parseFFprobeDurations(out string) (time.Duration, bool) {
	var best float64
	found := false
	for _, line := range strings.Fields(out) {
		v, err := strconv.ParseFloat(line, 64)
		if err != nil || v < 0 || v != v {
			continue
		}
		if !found || v > best {
			best, found = v, true
		}
	}
	if !found {
		return 0, false
	}
	const maxSec = float64(1<<62) / float64(time.Second)
	if best > maxSec {
		best = maxSec
	}
	return time.Duration(best * float64(time.Second)), true
}

var ffmpegDurationRE = regexp.MustCompile(`Duration:\s*(\d+):(\d{2}):(\d{2}(?:\.\d+)?)`)

func parseFFmpegDuration(out string) (time.Duration, bool) {
	m := ffmpegDurationRE.FindStringSubmatch(out)
	if m == nil {
		return 0, false
	}
	h, _ := strconv.ParseFloat(m[1], 64)
	mi, _ := strconv.ParseFloat(m[2], 64)
	s, _ := strconv.ParseFloat(m[3], 64)
	return time.Duration((h*3600 + mi*60 + s) * float64(time.Second)), true
}
