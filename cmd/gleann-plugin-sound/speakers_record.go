package main

import (
	"context"
	"fmt"
	"math"
	"strings"
	"sync"
	"time"

	"github.com/spf13/cobra"

	"github.com/tevfik/gleann-plugin-sound/internal/audio"
	"github.com/tevfik/gleann-plugin-sound/internal/config"
	"github.com/tevfik/gleann-plugin-sound/internal/speaker"
)

func newSpeakersRecordCmd() *cobra.Command {
	var seconds, maxWindows int
	var lang string
	cmd := &cobra.Command{
		Use:   "record <name>",
		Short: "Record your voice from the microphone and enrol it under a name",
		Long: `Records from the default microphone while you read a short text aloud, then
enrols the voice (replacing an earlier enrolment of the same name). Do it in the room
where you will record meetings, with nobody else talking and no TV or music on.
20-30 seconds are enough.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := strings.TrimSpace(args[0])
			if name == "" {
				return fmt.Errorf("a name is required")
			}
			if seconds < 10 || seconds > 120 {
				return fmt.Errorf("--seconds must be between 10 and 120")
			}
			emb, err := openEmbedder()
			if err != nil {
				return err
			}
			defer emb.Close()

			if lang == "" {
				if cfg := config.Load(); cfg != nil {
					lang = cfg.Language
				}
			}
			for i := 3; i > 0; i-- {
				drawRecordScreen(name, lang, seconds, fmt.Sprintf("Starting in %d... ", i))
				time.Sleep(time.Second)
			}
			drawRecordScreen(name, lang, seconds, "")

			pcm, err := recordMic(cmd.Context(), time.Duration(seconds)*time.Second)
			if err != nil {
				return err
			}
			if lvl := rms(pcm); lvl < 0.005 {
				return fmt.Errorf("the recording is silent (level %.4f): check the microphone with 'gleann-plugin-sound test'", lvl)
			}
			return enrollClips(emb, name, [][]int16{pcm}, maxWindows)
		},
	}
	cmd.Flags().StringVar(&lang, "lang", "", "language of the text to read: tr or en (default: the configured language, else en)")
	cmd.Flags().IntVar(&seconds, "seconds", 25, "how long to record")
	cmd.Flags().IntVar(&maxWindows, "windows", 12, "use at most this many 5 s windows")
	return cmd
}

// drawRecordScreen clears the terminal and shows the text to read, so it stays
// visible for the whole recording; the status line (countdown, then the level
// bar) is the last line and the only one redrawn afterwards.
func drawRecordScreen(name, lang string, seconds int, status string) {
	fmt.Print("\033[2J\033[H")
	fmt.Printf("Recording %d s for %q. Read this aloud, naturally:\n\n", seconds, name)
	for _, line := range wrapText(speaker.ReadingText(lang), 72) {
		fmt.Printf("  %s\n", line)
	}
	fmt.Print("\n" + status)
}

// wrapText breaks s into lines of at most width runes at word boundaries.
func wrapText(s string, width int) []string {
	var lines []string
	cur := ""
	for _, w := range strings.Fields(s) {
		if cur != "" && len([]rune(cur))+1+len([]rune(w)) > width {
			lines = append(lines, cur)
			cur = w
			continue
		}
		if cur != "" {
			cur += " "
		}
		cur += w
	}
	if cur != "" {
		lines = append(lines, cur)
	}
	return lines
}

// recordMic captures d of microphone audio, drawing a level bar meanwhile.
func recordMic(ctx context.Context, d time.Duration) ([]int16, error) {
	ctx, cancel := context.WithTimeout(ctx, d)
	defer cancel()

	var (
		mu   sync.Mutex
		pcm  []int16
		last []int16
	)
	capturer := audio.NewMalgoCapturer()
	err := capturer.Start(ctx, func(chunk []int16) {
		mu.Lock()
		pcm = append(pcm, chunk...)
		last = append(last[:0], chunk...)
		mu.Unlock()
	})
	if err != nil {
		return nil, fmt.Errorf("cannot start the microphone: %w", err)
	}
	defer func() { _ = capturer.Stop() }()

	start := time.Now()
	tick := time.NewTicker(100 * time.Millisecond)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			fmt.Print("\r\033[2K")
			mu.Lock()
			defer mu.Unlock()
			return append([]int16(nil), pcm...), nil
		case <-tick.C:
			mu.Lock()
			lvl := 0.0
			if len(last) > 0 {
				lvl = rms(last)
			}
			mu.Unlock()
			fmt.Printf("\r\033[2K%s %4.1f / %d s", dbBar(lvl, 30), time.Since(start).Seconds(), int(d.Seconds()))
		}
	}
}

// dbBar draws rms (0..1) on a -60..0 dBFS scale.
func dbBar(rms float64, width int) string {
	db := -60.0
	if rms > 0 {
		db = math.Max(-60, 20*math.Log10(rms))
	}
	n := int((db + 60) / 60 * float64(width))
	return "[" + strings.Repeat("█", n) + strings.Repeat("·", width-n) + "]"
}
