package main

import (
	"encoding/binary"
	"fmt"
	"math"
	"os"
	"sync"
	"time"

	"github.com/tevfik/gleann-plugin-sound/internal/pipeline"
)

// levelMeter measures the microphone signal of one dictation session so the
// log can say why nothing was typed: a dead or muted input, a very quiet one,
// or audio the recogniser did not take for speech.
type levelMeter struct {
	mu      sync.Mutex
	samples int
	peak    int
	sumSq   float64
	pcm     []int16 // the whole session, so a failed one can be replayed
}

// maxKeptSamples bounds the recording (two minutes of 16 kHz audio).
const maxKeptSamples = 2 * 60 * pipeline.SampleRate

// SaveWAV writes the session's audio as a 16 kHz mono WAV file.
func (m *levelMeter) SaveWAV(path string) error {
	m.mu.Lock()
	pcm := m.pcm
	m.mu.Unlock()
	if len(pcm) == 0 {
		return nil
	}
	n := uint32(len(pcm) * 2)
	hdr := make([]byte, 44)
	copy(hdr[0:], "RIFF")
	binary.LittleEndian.PutUint32(hdr[4:], 36+n)
	copy(hdr[8:], "WAVEfmt ")
	binary.LittleEndian.PutUint32(hdr[16:], 16)
	binary.LittleEndian.PutUint16(hdr[20:], 1) // PCM
	binary.LittleEndian.PutUint16(hdr[22:], 1) // mono
	binary.LittleEndian.PutUint32(hdr[24:], pipeline.SampleRate)
	binary.LittleEndian.PutUint32(hdr[28:], pipeline.SampleRate*2)
	binary.LittleEndian.PutUint16(hdr[32:], 2)
	binary.LittleEndian.PutUint16(hdr[34:], 16)
	copy(hdr[36:], "data")
	binary.LittleEndian.PutUint32(hdr[40:], n)
	body := make([]byte, n)
	for i, v := range pcm {
		binary.LittleEndian.PutUint16(body[i*2:], uint16(v))
	}
	return os.WriteFile(path, append(hdr, body...), 0o600)
}

func (m *levelMeter) Add(pcm []int16) {
	var peak int
	var sq float64
	for _, v := range pcm {
		a := int(v)
		if a < 0 {
			a = -a
		}
		if a > peak {
			peak = a
		}
		sq += float64(v) * float64(v)
	}
	m.mu.Lock()
	m.samples += len(pcm)
	m.peak = max(m.peak, peak)
	m.sumSq += sq
	if len(m.pcm) < maxKeptSamples {
		m.pcm = append(m.pcm, pcm...)
	}
	m.mu.Unlock()
}

func dbfs(x float64) float64 {
	if x <= 0 {
		return math.Inf(-1)
	}
	return 20 * math.Log10(x/32768)
}

// Summary describes the session in one line, with a hint when the input looks
// wrong. stats comes from pipeline.LiveConfig.Stats.
func (m *levelMeter) Summary(stats pipeline.LiveStats, typed bool) string {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.samples == 0 {
		return "[dictate] no audio arrived from the microphone — check the input device"
	}
	secs := time.Duration(m.samples) * time.Second / pipeline.SampleRate
	rms := math.Sqrt(m.sumSq / float64(m.samples))
	line := fmt.Sprintf("[dictate] heard %.1fs of audio (peak %.0f dBFS, average %.0f dBFS): %d/%d chunks speech, %d utterance(s)",
		secs.Seconds(), dbfs(float64(m.peak)), dbfs(rms), stats.SpeechChunks, stats.Chunks, stats.Utterances)
	switch {
	case typed || stats.Utterances > 0:
		return line
	case m.peak == 0:
		return line + " — the input is completely silent (muted, or the wrong input device)"
	case dbfs(float64(m.peak)) < -45:
		return line + " — the input is very quiet; raise the microphone level or speak closer"
	case stats.SpeechChunks == 0:
		return line + " — no speech detected; background noise may be as loud as your voice (use a headset or a closer microphone)"
	default:
		return line + " — speech was detected but no words were recognised"
	}
}
