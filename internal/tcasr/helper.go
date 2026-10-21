// Package tcasr runs transcribe.cpp (ggml GGUF models) as a sidecar process.
//
// It provides the "tc" backend: Nemotron 3.5 streaming with Q8 weights (more
// accurate than the int4 ONNX engine), and a Refiner that re-transcribes
// finished utterances with Qwen3-ASR. The sidecar is native/asr-helper; it
// is a separate process because whisper.cpp and transcribe.cpp each bring
// their own statically linked ggml.
package tcasr

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"sync"
	"time"
)

// HelperEnv overrides the helper location.
const HelperEnv = "GLEANN_ASR_HELPER"

// FindHelper locates gleann-asr-helper: $GLEANN_ASR_HELPER, next to this
// executable, ~/.gleann/bin, ~/.local/bin, then $PATH.
func FindHelper() (string, error) {
	if p := os.Getenv(HelperEnv); p != "" {
		if _, err := os.Stat(p); err != nil {
			return "", fmt.Errorf("%s=%s: %w", HelperEnv, p, err)
		}
		return p, nil
	}
	var cands []string
	if exe, err := os.Executable(); err == nil {
		cands = append(cands, filepath.Join(filepath.Dir(exe), "gleann-asr-helper"))
	}
	if home, err := os.UserHomeDir(); err == nil {
		cands = append(cands,
			filepath.Join(home, ".gleann", "bin", "gleann-asr-helper"),
			filepath.Join(home, ".local", "bin", "gleann-asr-helper"))
	}
	for _, c := range cands {
		if st, err := os.Stat(c); err == nil && !st.IsDir() {
			return c, nil
		}
	}
	if p, err := exec.LookPath("gleann-asr-helper"); err == nil {
		return p, nil
	}
	return "", errors.New("gleann-asr-helper not found: build it with 'make asr-helper' (see README) or set " + HelperEnv)
}

type reply struct {
	Text      string `json:"text"`
	Committed string `json:"committed"`
	Tentative string `json:"tentative"`
	MS        int64  `json:"ms"`
	Final     bool   `json:"final"`
	Ready     bool   `json:"ready"`
	OK        bool   `json:"ok"`
	Error     string `json:"error"`
}

// helper is one running sidecar. Calls are serialised.
type helper struct {
	cmd   *exec.Cmd
	in    io.WriteCloser
	out   *bufio.Reader
	mu    sync.Mutex
	dead  error
	label string
}

func startHelper(mode, model, lang string, attRight int) (*helper, error) {
	bin, err := FindHelper()
	if err != nil {
		return nil, err
	}
	args := []string{mode, model, lang}
	if mode == "stream" && attRight >= 0 {
		args = append(args, strconv.Itoa(attRight))
	}
	cmd := exec.Command(bin, args...)
	if os.Getenv("GLEANN_ASR_DEBUG") != "" {
		cmd.Stderr = os.Stderr
	}
	in, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start %s: %w", bin, err)
	}
	h := &helper{cmd: cmd, in: in, out: bufio.NewReaderSize(stdout, 1<<16), label: filepath.Base(model)}

	// Loading a 2 GB model can take a while; the first line says it is ready.
	type res struct {
		r   reply
		err error
	}
	ch := make(chan res, 1)
	go func() {
		var r reply
		line, err := h.out.ReadBytes('\n')
		if err == nil {
			err = json.Unmarshal(line, &r)
		}
		ch <- res{r, err}
	}()
	select {
	case r := <-ch:
		if r.err != nil || !r.r.Ready {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
			if r.err == nil {
				r.err = errors.New("unexpected first message")
			}
			return nil, fmt.Errorf("asr helper (%s) failed to load %s: %w", mode, model, r.err)
		}
	case <-time.After(3 * time.Minute):
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		return nil, fmt.Errorf("asr helper timed out loading %s", model)
	}
	log.Printf("[tc] %s helper ready: %s", mode, h.label)
	return h, nil
}

func (h *helper) call(op byte, pcm []int16) (reply, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.dead != nil {
		return reply{}, h.dead
	}
	var buf bytes.Buffer
	buf.WriteByte(op)
	if op == 'A' || op == 'T' {
		_ = binary.Write(&buf, binary.LittleEndian, uint32(len(pcm)))
		_ = binary.Write(&buf, binary.LittleEndian, pcm)
	}
	if _, err := h.in.Write(buf.Bytes()); err != nil {
		h.dead = fmt.Errorf("asr helper died: %w", err)
		return reply{}, h.dead
	}
	line, err := h.out.ReadBytes('\n')
	if err != nil {
		h.dead = fmt.Errorf("asr helper died: %w", err)
		return reply{}, h.dead
	}
	var r reply
	if err := json.Unmarshal(line, &r); err != nil {
		return reply{}, fmt.Errorf("bad helper reply %q: %w", bytes.TrimSpace(line), err)
	}
	if r.Error != "" {
		return r, errors.New(r.Error)
	}
	return r, nil
}

func (h *helper) close() error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.dead == nil {
		_, _ = h.in.Write([]byte{'Q'})
		h.dead = errors.New("closed")
	}
	_ = h.in.Close()
	done := make(chan struct{})
	go func() { _ = h.cmd.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		_ = h.cmd.Process.Kill()
		<-done
	}
	return nil
}
