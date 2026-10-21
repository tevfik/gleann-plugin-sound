package main

import (
	"context"
	"fmt"
	"math"
	"time"

	"github.com/spf13/cobra"

	"github.com/tevfik/gleann-plugin-sound/internal/config"
	"github.com/tevfik/gleann-plugin-sound/internal/core"
	"github.com/tevfik/gleann-plugin-sound/internal/speaker"
)

// closer lets openEmbedder return either the real Embedder or the stub's nil.
type embedCloser interface {
	speaker.Embed
	Close() error
}

func newSpeakersCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "speakers",
		Short: "Enrol voices so meetings are transcribed with names (enroll|list|remove|test)",
		Long: `Voice profiles live in ~/.gleann/speakers.json. Once someone is enrolled,
'listen' and 'plugin-serve' label every utterance with a name; voices that match
nobody (a TV, a stranger) are labelled "?". Identification by enrolled voice is far
more robust to background speech than clustering unknown voices.

Enrol each person with 15-30 s of clean speech in the room where you will record.`,
	}

	var maxWindows int
	enroll := &cobra.Command{
		Use:   "enroll <name> <audio-file>...",
		Short: "Learn a voice from one or more recordings of that person only",
		Args:  cobra.MinimumNArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]
			emb, err := openEmbedder()
			if err != nil {
				return err
			}
			defer emb.Close()

			var clips [][]int16
			for _, f := range args[1:] {
				pcm, err := core.DecodeFile(cmd.Context(), f)
				if err != nil {
					return fmt.Errorf("%s: %w", f, err)
				}
				clips = append(clips, pcm)
			}
			return enrollClips(emb, name, clips, maxWindows)
		},
	}
	enroll.Flags().IntVar(&maxWindows, "windows", 12, "use at most this many 5 s windows per file")

	list := &cobra.Command{
		Use:   "list",
		Short: "Show enrolled voices",
		RunE: func(cmd *cobra.Command, args []string) error {
			reg, err := speaker.LoadRegistry(speaker.DefaultPath())
			if err != nil {
				return err
			}
			if reg.Len() == 0 {
				fmt.Println("no voices enrolled (gleann-plugin-sound speakers enroll <name> <audio>)")
				return nil
			}
			for _, p := range reg.Profiles {
				fmt.Printf("%-20s %d samples\n", p.Name, p.Samples)
			}
			return nil
		},
	}

	remove := &cobra.Command{
		Use:   "remove <name>",
		Short: "Forget a voice",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			reg, err := speaker.LoadRegistry(speaker.DefaultPath())
			if err != nil {
				return err
			}
			if !reg.Remove(args[0]) {
				return fmt.Errorf("no voice named %q", args[0])
			}
			return reg.Save()
		},
	}

	var windowSec int
	var showTurns bool
	test := &cobra.Command{
		Use:   "test <audio-file>",
		Short: "Print who is speaking over time in a recording (checks enrolment and the threshold)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			emb, err := openEmbedder()
			if err != nil {
				return err
			}
			defer emb.Close()
			reg, err := speaker.LoadRegistry(speaker.DefaultPath())
			if err != nil {
				return err
			}
			pcm, err := core.DecodeFile(context.Background(), args[0])
			if err != nil {
				return err
			}
			var th float64
			if cfg := config.Load(); cfg != nil {
				th = cfg.SpeakerThreshold
			}
			l := &speaker.Labeler{Emb: emb, Reg: reg, Threshold: th}
			if showTurns {
				for _, tn := range l.Turns(pcm) {
					fmt.Printf("%s-%s  %-16s %.2f\n", fmtClock(tn.Start), fmtClock(tn.End), tn.Name, tn.Score)
				}
				return nil
			}
			win := windowSec * 16000
			for off := 0; off+win <= len(pcm); off += win {
				seg := pcm[off : off+win]
				if rms(seg) < 0.005 {
					continue
				}
				name, score := l.Label(seg)
				t0 := time.Duration(off) * time.Second / 16000
				fmt.Printf("%s  %-16s %.2f\n", fmtClock(t0), name, score)
			}
			return nil
		},
	}
	test.Flags().BoolVar(&showTurns, "turns", false, "show speaker turns found by change detection instead of fixed windows")
	test.Flags().IntVar(&windowSec, "window", 3, "analysis window in seconds")

	cmd.AddCommand(enroll, newSpeakersRecordCmd(), list, remove, test)
	return cmd
}

// speechWindows returns up to max evenly spaced windows of n samples whose
// level suggests speech (not silence).
func speechWindows(pcm []int16, n, max int) [][]int16 {
	var all [][]int16
	for off := 0; off+n <= len(pcm); off += n {
		w := pcm[off : off+n]
		if rms(w) >= 0.01 {
			all = append(all, w)
		}
	}
	if len(all) <= max {
		return all
	}
	out := make([][]int16, 0, max)
	for i := 0; i < max; i++ {
		out = append(out, all[i*len(all)/max])
	}
	return out
}

func rms(p []int16) float64 {
	var s float64
	for _, v := range p {
		f := float64(v) / 32768
		s += f * f
	}
	return math.Sqrt(s / float64(len(p)))
}

func fmtClock(d time.Duration) string {
	s := int(d.Seconds())
	return fmt.Sprintf("%02d:%02d", s/60, s%60)
}

// enrollClips embeds the speech windows of clips and stores them as name's
// voice (replacing an earlier enrolment of the same name).
func enrollClips(emb embedCloser, name string, clips [][]int16, maxWindows int) error {
	var samples [][]float32
	for _, pcm := range clips {
		for _, w := range speechWindows(pcm, 5*16000, maxWindows) {
			v, err := emb.Embed(w)
			if err != nil {
				return err
			}
			samples = append(samples, v)
		}
	}
	if len(samples) < 2 {
		return fmt.Errorf("found only %d usable 5 s speech windows: record at least ~15 s of the person speaking", len(samples))
	}
	reg, err := speaker.LoadRegistry(speaker.DefaultPath())
	if err != nil {
		return err
	}
	if err := reg.Enroll(name, samples); err != nil {
		return err
	}
	if err := reg.Save(); err != nil {
		return err
	}
	low := 2.0
	for _, s := range samples {
		if _, c := reg.Match(s); c < low {
			low = c
		}
	}
	fmt.Printf("Enrolled %q from %d windows. Consistency: lowest sample matches its profile at %.2f", name, len(samples), low)
	if low < 0.5 {
		fmt.Print(" (low: is it really one person, in clean audio?)")
	}
	fmt.Printf("\nStored in %s\n", speaker.DefaultPath())
	return nil
}
