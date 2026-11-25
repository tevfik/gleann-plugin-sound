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
	"context"
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
	"sync/atomic"
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
	// alive mirrors dead for lock-free readers (/health): a Refine call can
	// hold mu for a long time, and a health check must never wait on it.
	alive  atomic.Bool
	stdout io.Closer

	// done closes after the reaper goroutine has collected the process, so a
	// crashed child is never a zombie: it is reaped as soon as it exits.
	// waitErr is the exit error and may be read only after done closes.
	done    chan struct{}
	waitErr error
	// onExit is guarded by mu: a supervisor installs it while the reaper
	// may already be on its way out.
	onExit func(*helper)
	// exited mirrors done from the reaper's side (guarded by mu): once the
	// process has been collected, arming a hook is too late and must be
	// reported.
	exited bool
}

// markDead records the sidecar's death. Callers hold h.mu.
func (h *helper) markDead(err error) error {
	h.alive.Store(false)
	h.dead = err
	return err
}

// reap is the one and only cmd.Wait for this child: startHelper starts it as
// soon as the process exists. Wait also closes the helper's pipes, so a
// helper that died on its own and is replaced never leaks descriptors, and
// it sets cmd.ProcessState, so a crashed child is visibly collected. It flips alive the moment the sidecar dies, even idle, so /health never
// reports a dead helper as up. The exit hook installed by a supervisor runs
// before done closes, so anyone waiting on done sees a supervision state that
// already accounts for the death.
func (h *helper) reap() {
	h.waitErr = h.cmd.Wait()
	h.alive.Store(false)
	h.mu.Lock()
	h.exited = true
	fn := h.onExit
	h.mu.Unlock()
	if fn != nil {
		fn(h)
	}
	close(h.done)
}

// setOnExit arms the exit hook. Either the hook is installed and will run
// when the process goes away (true), or the process is already gone and the
// supervisor must handle the death itself (false). The answer is decided
// under the lock the reaper takes on its way out, so a death is never missed.
func (h *helper) setOnExit(fn func(*helper)) bool {
	if fn == nil {
		return false
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.exited || h.onExit != nil {
		return false
	}
	h.onExit = fn
	return true
}

// deadErr is the pipe error a call ran into, if any: the helper that answers
// "asr helper died" is on its way out even before the reaper has collected
// it, and a supervisor must not send that operation to a replacement without
// knowing which of the two it is looking at.
func (h *helper) deadErr() error {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.dead
}

// reaped waits until the reaper has collected the process. A helper whose
// pipe broke but whose process lingers past grace is killed. It is false only
// when ctx ends first.
func (h *helper) reaped(ctx context.Context, grace time.Duration) bool {
	t := time.NewTimer(grace)
	defer t.Stop()
	select {
	case <-h.done:
		return true
	case <-t.C:
		_ = h.cmd.Process.Kill()
	case <-ctx.Done():
		return false
	}
	select {
	case <-h.done:
		return true
	case <-ctx.Done():
		return false
	}
}

// died reports whether the process is gone, and why. Safe from any goroutine:
// waitErr is only read once done is closed.
func (h *helper) died() (bool, error) {
	select {
	case <-h.done:
		return true, h.waitErr
	default:
		return false, nil
	}
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
	h := &helper{cmd: cmd, in: in, out: bufio.NewReaderSize(stdout, 1<<16), done: make(chan struct{}), label: filepath.Base(model), stdout: stdout}
	h.alive.Store(true)
	go h.reap()

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
			<-h.done // the reaper collects it; nobody else may call Wait
			if r.err == nil {
				r.err = errors.New("unexpected first message")
			}
			return nil, fmt.Errorf("asr helper (%s) failed to load %s: %w", mode, model, r.err)
		}
	case <-time.After(3 * time.Minute):
		_ = cmd.Process.Kill()
		<-h.done
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
		return reply{}, h.markDead(fmt.Errorf("asr helper died: %w", err))
	}
	line, err := h.out.ReadBytes('\n')
	if err != nil {
		return reply{}, h.markDead(fmt.Errorf("asr helper died: %w", err))
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

// close asks the sidecar to quit and waits for the reaper to collect it. The
// helper lock is released before waiting, because a supervisor's exit hook
// (run by the reaper) takes that same lock.
func (h *helper) close() error {
	h.mu.Lock()
	h.alive.Store(false)
	if h.dead == nil {
		_, _ = h.in.Write([]byte{'Q'})
		h.dead = errors.New("closed")
	}
	_ = h.in.Close()
	h.mu.Unlock()
	select {
	case <-h.done:
	case <-time.After(500 * time.Millisecond): // main's S3 bound, inside the host grace
		_ = h.cmd.Process.Kill()
		<-h.done
	}
	_ = h.stdout.Close()
	return nil
}
