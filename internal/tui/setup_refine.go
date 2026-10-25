package tui

import (
	"fmt"
	"os"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/tevfik/gleann-plugin-sound/internal/config"
	"github.com/tevfik/gleann-plugin-sound/internal/tcasr"
)

// ── Preset (first step of a fresh setup) ───────────────────────
//
// A preset only pre-fills the later steps (models, backend, language,
// refiner); every one of them is still shown and can be changed.

type setupPreset struct {
	title string
	desc  string
}

var setupPresets = []setupPreset{
	{
		title: "Recommended — live dictation & meetings (Turkish / English)",
		desc: "Nemotron streaming (tc backend) types as you speak; Qwen3-ASR-1.7B checks each utterance. " +
			"Measured on conversational Turkish: 17 % word errors vs 39 % live only. ~3 GB download, needs gleann-asr-helper.",
	},
	{
		title: "Lightweight — Whisper Small Q5 (181 MB)",
		desc:  "Small download, no sidecar needed. Text appears after you release the key; less accurate on Turkish.",
	},
	{
		title: "Custom — choose every setting yourself",
		desc:  "Start from an empty model selection.",
	},
}

// systemLanguage returns "tr" or "en" from the locale environment, "" otherwise.
func systemLanguage() string {
	for _, k := range []string{"LC_ALL", "LC_MESSAGES", "LANG"} {
		v := strings.ToLower(os.Getenv(k))
		if v == "" {
			continue
		}
		switch {
		case strings.HasPrefix(v, "tr"):
			return "tr"
		case strings.HasPrefix(v, "en"):
			return "en"
		}
		return ""
	}
	return ""
}

func (m *SetupModel) selectAvailable(name string) {
	for i, a := range m.available {
		if a.Name == name {
			m.selected[i] = true
		}
	}
}

func (m *SetupModel) setBackend(name string) {
	for i, b := range m.backendOptions {
		if b == name {
			m.backendCursor = i
		}
	}
}

func (m *SetupModel) setLanguage(code string) {
	for i, l := range m.languages {
		if l.code == code {
			m.langCursor = i
		}
	}
}

// applyPreset pre-fills the wizard for preset i.
func (m *SetupModel) applyPreset(i int) {
	switch i {
	case 0:
		m.selectAvailable("silero-vad")
		m.selectAvailable("nemotron-3.5-asr-q8-gguf")
		m.selectAvailable(config.RecommendedRefineModel)
		m.setBackend("tc")
		m.existingDefaultModel = config.ModelPath("nemotron-3.5-asr-streaming-0.6b-Q8_0.gguf")
		for _, r := range config.AvailableRefineModels() {
			if r.Name == config.RecommendedRefineModel {
				m.refinePath = config.ModelPath(r.FileName)
			}
		}
		if lang := systemLanguage(); lang != "" {
			m.setLanguage(lang)
		} else {
			m.setLanguage("tr")
		}
	case 1:
		m.selectAvailable("silero-vad")
		m.selectAvailable("small-q5_1")
		m.setBackend("whisper")
		m.existingDefaultModel = config.ModelPath("ggml-small-q5_1.bin")
		m.refinePath = ""
		m.setLanguage(systemLanguage())
	}
}

func (m SetupModel) updatePreset(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "up", "k":
		if m.presetCursor > 0 {
			m.presetCursor--
		}
	case "down", "j":
		if m.presetCursor < len(setupPresets)-1 {
			m.presetCursor++
		}
	case "enter":
		m.applyPreset(m.presetCursor)
		m.phase = phaseModelSelect
	}
	return m, nil
}

func (m SetupModel) viewPreset() string {
	var b strings.Builder
	b.WriteString("  How will you use gleann-plugin-sound?\n")
	b.WriteString("  " + lipgloss.NewStyle().Foreground(ColorDimFg).Render("A preset fills in the next steps; you can still change each one.") + "\n\n")
	for i, p := range setupPresets {
		cursor := "  "
		style := NormalItemStyle
		if i == m.presetCursor {
			cursor, style = "▸ ", ActiveItemStyle
		}
		b.WriteString(style.Render(cursor+p.title) + "\n")
		b.WriteString(lipgloss.NewStyle().Foreground(ColorDimFg).Width(90).PaddingLeft(4).Render(p.desc) + "\n")
	}
	if m.presetCursor == 0 {
		b.WriteString(helperWarning())
	}
	b.WriteString("\n" + StatusBarStyle.Render("  ↑/↓ navigate • enter choose • esc quit"))
	return b.String()
}

// ── Refine model ───────────────────────────────────────────────

// refineOption is one row of the refiner step.
type refineOption struct {
	label    string
	path     string               // model to use (download target for download rows)
	download *config.WhisperModel // not downloaded yet: fetch it first
	custom   bool                 // type a path
	off      bool
}

func (m SetupModel) refineOptions() []refineOption {
	var opts []refineOption
	for _, r := range config.AvailableRefineModels() {
		r := r
		path := config.ModelPath(r.FileName)
		if config.IsModelDownloaded(r.FileName) {
			opts = append(opts, refineOption{label: "Qwen3-ASR-1.7B Q8 — recommended ✓ downloaded", path: path})
		} else {
			opts = append(opts, refineOption{label: "Download Qwen3-ASR-1.7B Q8 (" + r.Size + ") and use it — recommended", path: path, download: &r})
		}
	}
	opts = append(opts,
		refineOption{label: "Use my own GGUF file…", custom: true},
		refineOption{label: "Off — type live words immediately (faster, far less accurate)", off: true},
	)
	return opts
}

// refineApplies reports whether the backend uses a refiner: only the live
// (streaming) backends do.
func (m SetupModel) refineApplies() bool {
	b := m.backendOptions[m.backendCursor]
	return b == "tc" || b == "nemotron"
}

// prefillRefineCursor points the cursor at the current refine_model.
func (m *SetupModel) prefillRefineCursor() {
	opts := m.refineOptions()
	for i, o := range opts {
		switch {
		case m.refinePath == "" && o.off,
			m.refinePath != "" && o.path == m.refinePath:
			m.refineCursor = i
			return
		}
	}
	if m.refinePath != "" { // a file outside the catalog
		for i, o := range opts {
			if o.custom {
				m.refineCursor, m.refineInput = i, m.refinePath
			}
		}
	}
}

func (m SetupModel) updateRefine(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if m.refineCustom {
		switch msg.String() {
		case "enter":
			p := config.ExpandPath(strings.TrimSpace(m.refineInput))
			if _, err := os.Stat(p); err != nil {
				m.refineErr = fmt.Sprintf("cannot read %s", p)
				return m, nil
			}
			m.refinePath, m.refineCustom, m.refineErr = p, false, ""
			m.finishPhase()
		case "backspace":
			if r := []rune(m.refineInput); len(r) > 0 {
				m.refineInput = string(r[:len(r)-1])
			}
		default:
			if msg.Type == tea.KeyRunes {
				m.refineInput += string(msg.Runes)
			}
		}
		return m, nil
	}

	opts := m.refineOptions()
	switch msg.String() {
	case "up", "k":
		if m.refineCursor > 0 {
			m.refineCursor--
		}
	case "down", "j":
		if m.refineCursor < len(opts)-1 {
			m.refineCursor++
		}
	case "enter":
		o := opts[m.refineCursor]
		m.refineErr = ""
		switch {
		case o.off:
			m.refinePath = ""
			m.finishPhase()
		case o.custom:
			m.refineCustom = true
		case o.download != nil:
			// Fetch it, then come back here with the model downloaded.
			m.refinePath = o.path
			m.afterDownload = phaseRefine
			m.downloadQ = nil
			m.downloading = o.download.DisplayName
			m.phase = phaseDownloading
			m.progressCh = make(chan config.DownloadProgress, 50)
			return m, startDownloadModel(*o.download, m.progressCh)
		default:
			m.refinePath = o.path
			m.finishPhase()
		}
	}
	return m, nil
}

func (m SetupModel) viewRefine() string {
	var b strings.Builder
	b.WriteString("  Accuracy — refine model:\n")
	b.WriteString("  " + lipgloss.NewStyle().Foreground(ColorDimFg).Width(90).Render(
		"A second, more accurate model re-transcribes each utterance when you pause. Dictation then types the "+
			"checked text after each pause instead of word by word. Measured on conversational Turkish: "+
			"live words 39 % word errors, refined 17 %.") + "\n\n")

	if !m.refineApplies() {
		b.WriteString(lipgloss.NewStyle().Foreground(ColorAccent).Render(
			"  Only the live backends (tc, nemotron) use a refiner; the current backend ignores this setting.") + "\n\n")
	}

	for i, o := range m.refineOptions() {
		cursor := "  "
		style := NormalItemStyle
		if i == m.refineCursor {
			cursor, style = "▸ ", ActiveItemStyle
		}
		b.WriteString(style.Render(cursor+o.label) + "\n")
	}
	if m.refineCustom {
		b.WriteString("\n" + lipgloss.NewStyle().Foreground(ColorAccent).Render("  GGUF path: "+m.refineInput+"█") + "\n")
		b.WriteString("  " + lipgloss.NewStyle().Foreground(ColorDimFg).Render("A Qwen3-ASR GGUF, e.g. from huggingface.co/handy-computer") + "\n")
	}
	if m.refineErr != "" {
		b.WriteString("\n" + ErrorBadge.Render("  "+m.refineErr) + "\n")
	}
	b.WriteString(helperWarning())
	if m.refineCustom {
		b.WriteString("\n" + StatusBarStyle.Render("  type the path • enter use it • esc back to the list"))
	} else {
		b.WriteString("\n" + StatusBarStyle.Render("  ↑/↓ navigate • enter select • esc back"))
	}
	return b.String()
}

// refineSummary is the refiner setting for the overview and summary.
func (m SetupModel) refineSummary() string {
	if m.refinePath == "" {
		return "off"
	}
	name := m.refinePath
	if i := strings.LastIndex(name, "/"); i >= 0 {
		name = name[i+1:]
	}
	if !m.refineApplies() {
		name += " (unused by this backend)"
	}
	return name
}

// helperWarning explains how to get the sidecar the tc backend and the
// refiner run in, when it is missing.
func helperWarning() string {
	if _, err := tcasr.FindHelper(); err == nil {
		return ""
	}
	return "\n" + lipgloss.NewStyle().Foreground(ColorAccent).Width(90).Render(
		"  ⚠ gleann-asr-helper was not found. The tc backend and the refiner run in it. Install it from the "+
			"release (gleann-asr-helper-<version>-<os>-<arch>.tar.gz, into ~/.local/bin) or build it from the "+
			"source tree with 'make asr-helper' (needs cmake and libopenblas-dev).") + "\n"
}
