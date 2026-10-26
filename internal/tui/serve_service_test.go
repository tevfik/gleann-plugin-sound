package tui

import (
	"strings"
	"testing"

	"github.com/tevfik/gleann-plugin-sound/internal/config"
)

func TestServeServiceQuoting(t *testing.T) {
	if got := quoteSystemd("plain"); got != "plain" {
		t.Errorf("quoteSystemd(plain) = %q", got)
	}
	if got := quoteSystemd(`/home/a b/bin "x"`); got != `"/home/a b/bin \"x\""` {
		t.Errorf("quoteSystemd = %q", got)
	}
	if got := xmlEscape("a&b<c>"); got != "a&amp;b&lt;c&gt;" {
		t.Errorf("xmlEscape = %q", got)
	}
}

func TestServeMenuItemIsToggle(t *testing.T) {
	it := serveMenuItem()
	if !strings.HasPrefix(it.title, "Serve (") {
		t.Errorf("serve item title %q should show Enable/Disable", it.title)
	}
}

func TestSpeakersToggleMatchPersists(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	if err := config.Save(&config.Config{Completed: true}); err != nil {
		t.Fatal(err)
	}
	restarts := 0
	restartDictate = func() (bool, error) { restarts++; return true, nil }
	t.Cleanup(func() { restartDictate = RestartDaemon })
	m := moveTo(t, NewSpeakersModel(t.TempDir()+"/speakers.json"), rowMatch)
	for _, r := range m.rows() {
		if r.kind == rowThreshold {
			t.Fatal("threshold row shown while matching is off")
		}
	}
	if m.matchTh() != config.DefaultDictateSpeakerThreshold {
		t.Errorf("default threshold %.2f", m.matchTh())
	}
	m = press(m, keyEnter)
	if !m.matchOn() || !config.Load().DictateSpeakerMatch {
		t.Fatal("toggle on was not saved")
	}
	m = press(m, keyDown) // the threshold row appears under the toggle
	if m.rows()[m.cursor].kind != rowThreshold {
		t.Fatalf("row under the toggle is %d", m.rows()[m.cursor].kind)
	}
	m = press(m, keyRight)
	if got := config.Load().DictateThreshold(); got != 0.5 || m.matchTh() != 0.5 {
		t.Errorf("after →: saved %.2f, shown %.2f, want 0.50", got, m.matchTh())
	}
	m = press(m, keyLeft, keyLeft)
	if got := config.Load().DictateThreshold(); got != 0.4 {
		t.Errorf("after ← ←: %.2f, want 0.40", got)
	}
	m = press(m, keyEnter) // reset to the default, stored as "unset"
	if c := config.Load(); c.DictateSpeakerThreshold != 0 || m.matchTh() != config.DefaultDictateSpeakerThreshold {
		t.Errorf("reset: stored %.2f, shown %.2f", c.DictateSpeakerThreshold, m.matchTh())
	}
	m = press(m, keyRight, keyLeft) // back on the default: stored as unset too
	if c := config.Load(); c.DictateSpeakerThreshold != 0 {
		t.Errorf("default value stored explicitly: %.2f", c.DictateSpeakerThreshold)
	}
	m = press(m, keyUp, keyEnter)
	if m.matchOn() || config.Load().DictateSpeakerMatch {
		t.Fatal("toggle off was not saved")
	}
	if restarts != 8 {
		t.Errorf("daemon restarted %d times, want once per change", restarts)
	}
}

func TestStepThresholdBounds(t *testing.T) {
	prefs := config.Settings()
	c := &config.Config{DictateSpeakerThreshold: 0.80}
	if _ = prefs.Step(c, keyThreshold, +1); c.DictateThreshold() != 0.80 {
		t.Errorf("upper bound: %.2f", c.DictateThreshold())
	}
	c.DictateSpeakerThreshold = 0.30
	if _ = prefs.Step(c, keyThreshold, -1); c.DictateThreshold() != 0.30 {
		t.Errorf("lower bound: %.2f", c.DictateThreshold())
	}
}
