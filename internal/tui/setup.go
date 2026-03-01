package tui

import (
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"

	"github.com/charmbracelet/bubbles/spinner"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/tevfik/gleann-sound/internal/config"
)

// ── Wizard phases ──────────────────────────────────────────────

type setupPhase int

const (
	phaseModelSelect   setupPhase = iota // select models to download
	phaseDownloading                     // downloading models
	phaseDefaultModel                    // choose default model
	phaseLanguage                        // language selection
	phaseHotkey                          // hotkey configuration
	phaseGRPC                            // gRPC server configuration
	phaseBackend                         // backend selection (whisper / onnx)
	phaseOutput                          // output directory for transcriptions
	phaseAudioSource                     // audio source selection (mic / speaker / both)
	phaseSummary                         // summary & confirm
)

// ── Messages ───────────────────────────────────────────────────

type downloadDoneMsg struct {
	model config.WhisperModel
	err   error
}

type allDownloadsDoneMsg struct{}

// ── SetupModel ─────────────────────────────────────────────────

type SetupModel struct {
	phase     setupPhase
	width     int
	height    int
	cancelled bool
	done      bool

	// Model selection (multi-select).
	available   []config.WhisperModel
	selected    map[int]bool // indices into available
	modelCursor int

	// Download progress.
	spinner     spinner.Model
	downloading string // current model name
	downloaded  []string
	downloadErr string
	downloadQ   []config.WhisperModel // queue of models to download

	// Default model.
	installedModels      []config.ModelEntry
	defaultCursor        int
	existingDefaultModel string // path from existing config, used for pre-filling cursor

	// Language.
	languages     []langOption
	langCursor    int

	// Hotkey (preset selection).
	hotkeyPresets []hotkeyOption // available hotkey presets
	hotkeyCursor  int            // cursor position in preset list
	hotkeyCustom  bool           // user is typing custom hotkey string
	hotkeyInput   string         // custom text input buffer

	// gRPC server.
	grpcEnabled bool   // whether gRPC server is enabled alongside dictation
	grpcAddr    string // listen address (e.g. "localhost:50051")
	grpcEditing bool   // user is editing the address

	// Backend selection.
	backendOptions []string // available backends
	backendCursor  int      // cursor position

	// Output directory.
	outputDir     string // output directory for transcription files
	outputEditing bool   // user is editing the path

	// Audio source.
	audioSourceOptions []string // available sources (mic, speaker, both)
	audioSourceCursor  int      // cursor position

	// Result.
	result *config.Config
}

type langOption struct {
	code string
	name string
}

var defaultLanguages = []langOption{
	{code: "", name: "Auto-detect"},
	{code: "en", name: "English"},
	{code: "tr", name: "Türkçe"},
	{code: "de", name: "Deutsch"},
	{code: "fr", name: "Français"},
	{code: "es", name: "Español"},
	{code: "it", name: "Italiano"},
	{code: "pt", name: "Português"},
	{code: "ru", name: "Русский"},
	{code: "ja", name: "日本語"},
	{code: "zh", name: "中文"},
	{code: "ko", name: "한국어"},
	{code: "ar", name: "العربية"},
}

type hotkeyOption struct {
	label string
	value string // empty string means "Custom…"
}

var defaultHotkeyPresets = []hotkeyOption{
	{label: "Ctrl+Shift+Space  (recommended)", value: "ctrl+shift+space"},
	{label: "Ctrl+Alt+Space", value: "ctrl+alt+space"},
	{label: "Ctrl+Space", value: "ctrl+space"},
	{label: "Super+Space", value: "super+space"},
	{label: "F9", value: "f9"},
	{label: "F10", value: "f10"},
	{label: "Ctrl+F9", value: "ctrl+f9"},
	{label: "Custom…", value: ""},
}

func NewSetupModel(existingCfg *config.Config) SetupModel {
	s := spinner.New()
	s.Spinner = spinner.Dot
	s.Style = lipgloss.NewStyle().Foreground(ColorSecondary)

	available := config.AvailableModels()
	selected := make(map[int]bool)

	// Pre-select already downloaded models.
	for i, m := range available {
		if config.IsModelDownloaded(m.FileName) {
			selected[i] = true
		}
	}

	// Pre-fill from existing config.
	langIdx := 0
	hotkeyCursor := 0
	hotkeyCustom := false
	hotkeyInput := ""
	existingDefault := ""
	grpcEnabled := false
	grpcAddr := "localhost:50051"
	backendCursor := 0 // 0 = whisper
	outputDir := "~/.gleann/transcriptions"
	audioSourceCursor := 0 // 0 = mic
	if existingCfg != nil {
		existingDefault = existingCfg.DefaultModel
		if existingCfg.GRPCAddr != "" {
			grpcEnabled = true
			grpcAddr = existingCfg.GRPCAddr
		}
		if existingCfg.Backend == "onnx" {
			backendCursor = 1
		}
		if existingCfg.OutputDir != "" {
			outputDir = existingCfg.OutputDir
		}
		switch existingCfg.AudioSource {
		case "speaker":
			audioSourceCursor = 1
		case "both":
			audioSourceCursor = 2
		}
		for i, l := range defaultLanguages {
			if l.code == existingCfg.Language {
				langIdx = i
				break
			}
		}
		// Match existing hotkey to a preset.
		if existingCfg.Hotkey != "" {
			matched := false
			for i, p := range defaultHotkeyPresets {
				if p.value == existingCfg.Hotkey {
					hotkeyCursor = i
					matched = true
					break
				}
			}
			if !matched {
				hotkeyCursor = len(defaultHotkeyPresets) - 1
				hotkeyCustom = true
				hotkeyInput = existingCfg.Hotkey
			}
		}
	}

	return SetupModel{
		phase:                phaseModelSelect,
		available:            available,
		selected:             selected,
		spinner:              s,
		languages:            defaultLanguages,
		langCursor:           langIdx,
		hotkeyPresets:        defaultHotkeyPresets,
		hotkeyCursor:         hotkeyCursor,
		hotkeyCustom:         hotkeyCustom,
		hotkeyInput:          hotkeyInput,
		existingDefaultModel: existingDefault,
		grpcEnabled:          grpcEnabled,
		grpcAddr:             grpcAddr,
		backendOptions:       []string{"whisper", "onnx"},
		backendCursor:        backendCursor,
		outputDir:            outputDir,
		audioSourceOptions:   []string{"mic", "speaker", "both"},
		audioSourceCursor:    audioSourceCursor,
	}
}

func (m SetupModel) Init() tea.Cmd {
	return m.spinner.Tick
}

func (m SetupModel) Cancelled() bool { return m.cancelled }
func (m SetupModel) Done() bool      { return m.done }
func (m SetupModel) Result() *config.Config { return m.result }

// ── Update ─────────────────────────────────────────────────────

func (m SetupModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		return m, nil

	case spinner.TickMsg:
		var cmd tea.Cmd
		m.spinner, cmd = m.spinner.Update(msg)
		return m, cmd

	case downloadDoneMsg:
		if msg.err != nil {
			m.downloadErr = fmt.Sprintf("Failed to download %s: %v", msg.model.Name, msg.err)
		} else {
			m.downloaded = append(m.downloaded, msg.model.Name)
			entry := config.ModelEntry{
				Name: msg.model.Name,
				Path: config.ModelPath(msg.model.FileName),
				Size: msg.model.Size,
			}
			if msg.model.Multilingual {
				entry.Language = "multilingual"
			} else {
				entry.Language = "en"
			}
			m.installedModels = append(m.installedModels, entry)
		}
		// Download next in queue.
		if len(m.downloadQ) > 0 {
			next := m.downloadQ[0]
			m.downloadQ = m.downloadQ[1:]
			m.downloading = next.DisplayName
			return m, downloadModel(next)
		}
		// All done — also add pre-existing models.
		m.addPreExistingModels()
		if len(m.installedModels) == 0 {
			m.downloadErr = "No models downloaded. At least one model is required."
			m.phase = phaseModelSelect
			return m, nil
		}
		m.prefillDefaultCursor()
		m.phase = phaseDefaultModel
		return m, nil

	case tea.KeyMsg:
		if msg.String() == "ctrl+c" {
			m.cancelled = true
			return m, tea.Quit
		}
		if msg.String() == "esc" {
			// In hotkey custom input mode, esc returns to preset list.
			if m.phase == phaseHotkey && m.hotkeyCustom {
				m.hotkeyCustom = false
				return m, nil
			}
			if m.phase > phaseModelSelect && m.phase != phaseDownloading {
				m.phase--
				if m.phase == phaseDownloading {
					m.phase-- // skip download phase when going back
				}
				return m, nil
			}
			m.cancelled = true
			return m, tea.Quit
		}
		return m.handlePhaseKey(msg)
	}
	return m, nil
}

func (m SetupModel) handlePhaseKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch m.phase {
	case phaseModelSelect:
		return m.updateModelSelect(msg)
	case phaseDefaultModel:
		return m.updateDefaultModel(msg)
	case phaseLanguage:
		return m.updateLanguage(msg)
	case phaseHotkey:
		return m.updateHotkey(msg)
	case phaseGRPC:
		return m.updateGRPC(msg)
	case phaseBackend:
		return m.updateBackend(msg)
	case phaseOutput:
		return m.updateOutput(msg)
	case phaseAudioSource:
		return m.updateAudioSource(msg)
	case phaseSummary:
		return m.updateSummary(msg)
	}
	return m, nil
}

// ── Model Select ───────────────────────────────────────────────

func (m SetupModel) updateModelSelect(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "up", "k":
		if m.modelCursor > 0 {
			m.modelCursor--
		}
	case "down", "j":
		if m.modelCursor < len(m.available)-1 {
			m.modelCursor++
		}
	case " ":
		m.selected[m.modelCursor] = !m.selected[m.modelCursor]
	case "enter":
		// Build download queue (only models not yet downloaded).
		var queue []config.WhisperModel
		for i, sel := range m.selected {
			if sel && !config.IsModelDownloaded(m.available[i].FileName) {
				queue = append(queue, m.available[i])
			}
		}
		if len(queue) == 0 {
			// All selected models already downloaded, skip to default.
			m.addPreExistingModels()
			if len(m.installedModels) == 0 {
				// Nothing selected at all.
				return m, nil
			}
			m.prefillDefaultCursor()
			m.phase = phaseDefaultModel
			return m, nil
		}
		m.downloadQ = queue[1:]
		m.downloading = queue[0].DisplayName
		m.phase = phaseDownloading
		return m, downloadModel(queue[0])
	}
	return m, nil
}

// prefillDefaultCursor sets defaultCursor to the index of the existing
// default model in installedModels so the user sees their previous choice
// pre-selected.
func (m *SetupModel) prefillDefaultCursor() {
	if m.existingDefaultModel == "" {
		return
	}
	for i, e := range m.installedModels {
		if e.Path == m.existingDefaultModel {
			m.defaultCursor = i
			return
		}
	}
}

func (m *SetupModel) addPreExistingModels() {
	existing := make(map[string]bool)
	for _, e := range m.installedModels {
		existing[e.Name] = true
	}
	// Iterate in stable order (by available model index, not map order).
	for i := 0; i < len(m.available); i++ {
		if !m.selected[i] {
			continue
		}
		if existing[m.available[i].Name] {
			continue
		}
		if config.IsModelDownloaded(m.available[i].FileName) {
			entry := config.ModelEntry{
				Name: m.available[i].Name,
				Path: config.ModelPath(m.available[i].FileName),
				Size: m.available[i].Size,
			}
			if m.available[i].Multilingual {
				entry.Language = "multilingual"
			} else {
				entry.Language = "en"
			}
			m.installedModels = append(m.installedModels, entry)
		}
	}
}

// ── Default Model ──────────────────────────────────────────────

func (m SetupModel) updateDefaultModel(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "up", "k":
		if m.defaultCursor > 0 {
			m.defaultCursor--
		}
	case "down", "j":
		if m.defaultCursor < len(m.installedModels)-1 {
			m.defaultCursor++
		}
	case "enter":
		m.phase = phaseLanguage
	}
	return m, nil
}

// ── Language ───────────────────────────────────────────────────

func (m SetupModel) updateLanguage(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "up", "k":
		if m.langCursor > 0 {
			m.langCursor--
		}
	case "down", "j":
		if m.langCursor < len(m.languages)-1 {
			m.langCursor++
		}
	case "enter":
		m.phase = phaseHotkey
	}
	return m, nil
}

// ── Hotkey (preset selection) ──────────────────────────────────

func (m SetupModel) updateHotkey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	// Custom text input mode.
	if m.hotkeyCustom {
		key := msg.String()
		switch key {
		case "enter":
			if m.hotkeyInput != "" {
				m.phase = phaseGRPC
			}
		case "backspace":
			if len(m.hotkeyInput) > 0 {
				m.hotkeyInput = m.hotkeyInput[:len(m.hotkeyInput)-1]
			} else {
				m.hotkeyCustom = false // back to preset list
			}
		default:
			// Accept printable ASCII for building the hotkey string.
			if len(key) == 1 && key[0] >= 32 && key[0] <= 126 {
				m.hotkeyInput += key
			}
		}
		return m, nil
	}

	// Preset list selection.
	switch msg.String() {
	case "up", "k":
		if m.hotkeyCursor > 0 {
			m.hotkeyCursor--
		}
	case "down", "j":
		if m.hotkeyCursor < len(m.hotkeyPresets)-1 {
			m.hotkeyCursor++
		}
	case "enter":
		preset := m.hotkeyPresets[m.hotkeyCursor]
		if preset.value == "" {
			// "Custom…" selected — switch to text input.
			m.hotkeyCustom = true
		} else {
			m.phase = phaseGRPC
		}
	}
	return m, nil
}

// ── gRPC ───────────────────────────────────────────────────────

func (m SetupModel) updateGRPC(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	// Address editing mode.
	if m.grpcEditing {
		key := msg.String()
		switch key {
		case "enter":
			if m.grpcAddr != "" {
				m.grpcEditing = false
				m.phase = phaseBackend
			}
		case "backspace":
			if len(m.grpcAddr) > 0 {
				m.grpcAddr = m.grpcAddr[:len(m.grpcAddr)-1]
			}
		case "esc":
			m.grpcEditing = false
		default:
			if len(key) == 1 && key[0] >= 32 && key[0] <= 126 {
				m.grpcAddr += key
			}
		}
		return m, nil
	}

	// Toggle / navigation.
	switch msg.String() {
	case "up", "k", "down", "j", " ":
		m.grpcEnabled = !m.grpcEnabled
	case "e":
		if m.grpcEnabled {
			m.grpcEditing = true
		}
	case "enter":
		if !m.grpcEnabled {
			m.grpcAddr = ""
		}
		m.phase = phaseBackend
	}
	return m, nil
}

// ── Backend ────────────────────────────────────────────────────

func (m SetupModel) updateBackend(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "up", "k":
		if m.backendCursor > 0 {
			m.backendCursor--
		}
	case "down", "j":
		if m.backendCursor < len(m.backendOptions)-1 {
			m.backendCursor++
		}
	case "enter":
		m.phase = phaseOutput
	}
	return m, nil
}

// ── Output Directory ───────────────────────────────────────────

func (m SetupModel) updateOutput(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if m.outputEditing {
		key := msg.String()
		switch key {
		case "enter":
			if m.outputDir != "" {
				m.outputEditing = false
				m.phase = phaseAudioSource
			}
		case "backspace":
			if len(m.outputDir) > 0 {
				m.outputDir = m.outputDir[:len(m.outputDir)-1]
			}
		case "esc":
			m.outputEditing = false
		default:
			if len(key) == 1 && key[0] >= 32 && key[0] <= 126 {
				m.outputDir += key
			}
		}
		return m, nil
	}

	switch msg.String() {
	case "e":
		m.outputEditing = true
	case "enter":
		m.phase = phaseAudioSource
	}
	return m, nil
}

// ── Audio Source ───────────────────────────────────────────────

func (m SetupModel) updateAudioSource(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "up", "k":
		if m.audioSourceCursor > 0 {
			m.audioSourceCursor--
		}
	case "down", "j":
		if m.audioSourceCursor < len(m.audioSourceOptions)-1 {
			m.audioSourceCursor++
		}
	case "enter":
		m.phase = phaseSummary
	}
	return m, nil
}

// selectedHotkey returns the currently selected hotkey string.
func (m SetupModel) selectedHotkey() string {
	if m.hotkeyCustom && m.hotkeyInput != "" {
		return m.hotkeyInput
	}
	if m.hotkeyCursor < len(m.hotkeyPresets) && m.hotkeyPresets[m.hotkeyCursor].value != "" {
		return m.hotkeyPresets[m.hotkeyCursor].value
	}
	return "ctrl+shift+space"
}

// ── Summary ────────────────────────────────────────────────────

func (m SetupModel) updateSummary(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "enter", "y":
		m.done = true
		m.result = m.buildConfig()
		return m, tea.Quit
	case "n":
		m.cancelled = true
		return m, tea.Quit
	}
	return m, nil
}

func (m SetupModel) buildConfig() *config.Config {
	defaultModel := ""
	if m.defaultCursor < len(m.installedModels) {
		defaultModel = m.installedModels[m.defaultCursor].Path
	}

	lang := ""
	if m.langCursor < len(m.languages) {
		lang = m.languages[m.langCursor].code
	}

	hotkey := m.selectedHotkey()

	cfg := &config.Config{
		DefaultModel: defaultModel,
		Language:      lang,
		Hotkey:        hotkey,
		Models:        m.installedModels,
		Backend:       m.backendOptions[m.backendCursor],
		AudioSource:   m.audioSourceOptions[m.audioSourceCursor],
		OutputDir:     m.outputDir,
		Completed:     true,
	}
	if m.grpcEnabled && m.grpcAddr != "" {
		cfg.GRPCAddr = m.grpcAddr
	}
	return cfg
}

// ── Download command ───────────────────────────────────────────

func downloadModel(model config.WhisperModel) tea.Cmd {
	return func() tea.Msg {
		dir := config.ModelsDir()
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return downloadDoneMsg{model: model, err: err}
		}

		dest := config.ModelPath(model.FileName)

		// Skip if already exists.
		if _, err := os.Stat(dest); err == nil {
			return downloadDoneMsg{model: model, err: nil}
		}

		resp, err := http.Get(model.URL)
		if err != nil {
			return downloadDoneMsg{model: model, err: err}
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			return downloadDoneMsg{model: model, err: fmt.Errorf("HTTP %d", resp.StatusCode)}
		}

		f, err := os.Create(dest + ".tmp")
		if err != nil {
			return downloadDoneMsg{model: model, err: err}
		}

		if _, err := io.Copy(f, resp.Body); err != nil {
			f.Close()
			os.Remove(dest + ".tmp")
			return downloadDoneMsg{model: model, err: err}
		}
		f.Close()

		if err := os.Rename(dest+".tmp", dest); err != nil {
			return downloadDoneMsg{model: model, err: err}
		}

		return downloadDoneMsg{model: model, err: nil}
	}
}

// ── View ───────────────────────────────────────────────────────

func (m SetupModel) View() string {
	var b strings.Builder
	b.WriteString(TitleStyle.Render(" gleann-sound Setup "))
	b.WriteString("\n\n")

	switch m.phase {
	case phaseModelSelect:
		b.WriteString(m.viewModelSelect())
	case phaseDownloading:
		b.WriteString(m.viewDownloading())
	case phaseDefaultModel:
		b.WriteString(m.viewDefaultModel())
	case phaseLanguage:
		b.WriteString(m.viewLanguage())
	case phaseHotkey:
		b.WriteString(m.viewHotkey())
	case phaseGRPC:
		b.WriteString(m.viewGRPC())
	case phaseBackend:
		b.WriteString(m.viewBackend())
	case phaseOutput:
		b.WriteString(m.viewOutput())
	case phaseAudioSource:
		b.WriteString(m.viewAudioSource())
	case phaseSummary:
		b.WriteString(m.viewSummary())
	}

	return b.String()
}

func (m SetupModel) viewModelSelect() string {
	var b strings.Builder
	b.WriteString("  Select Whisper models to download:\n")
	b.WriteString("  " + lipgloss.NewStyle().Foreground(ColorDimFg).Render("(space to toggle, enter to continue)") + "\n\n")

	for i, model := range m.available {
		cursor := "  "
		if i == m.modelCursor {
			cursor = "▸ "
		}

		check := "○"
		checkStyle := CheckboxUnchecked
		if m.selected[i] {
			check = "●"
			checkStyle = CheckboxChecked
		}

		downloaded := ""
		if config.IsModelDownloaded(model.FileName) {
			downloaded = SuccessBadge.Render(" ✓ downloaded")
		}

		label := model.DisplayName
		if !model.Multilingual {
			label += " 🇬🇧"
		} else {
			label += " 🌍"
		}

		if i == m.modelCursor {
			b.WriteString(ActiveItemStyle.Render(fmt.Sprintf("%s%s %s%s", cursor, checkStyle.Render(check), label, downloaded)))
		} else {
			b.WriteString(NormalItemStyle.Render(fmt.Sprintf("%s%s %s%s", cursor, checkStyle.Render(check), label, downloaded)))
		}
		b.WriteString("\n")
	}

	b.WriteString("\n")
	b.WriteString(StatusBarStyle.Render("  ↑/↓ navigate • space toggle • enter continue • esc back"))
	return b.String()
}

func (m SetupModel) viewDownloading() string {
	var b strings.Builder
	b.WriteString(fmt.Sprintf("  %s Downloading: %s\n\n", m.spinner.View(), m.downloading))

	for _, name := range m.downloaded {
		b.WriteString(SuccessBadge.Render(fmt.Sprintf("  ✓ %s\n", name)))
	}

	if m.downloadErr != "" {
		b.WriteString(ErrorBadge.Render(fmt.Sprintf("  ✗ %s\n", m.downloadErr)))
	}

	b.WriteString("\n")
	b.WriteString(StatusBarStyle.Render("  Please wait..."))
	return b.String()
}

func (m SetupModel) viewDefaultModel() string {
	var b strings.Builder
	b.WriteString("  Select default model:\n\n")

	for i, model := range m.installedModels {
		cursor := "  "
		if i == m.defaultCursor {
			cursor = "▸ "
		}

		label := fmt.Sprintf("%s (%s, %s)", model.Name, model.Size, model.Language)
		if i == m.defaultCursor {
			b.WriteString(ActiveItemStyle.Render(cursor + label))
		} else {
			b.WriteString(NormalItemStyle.Render(cursor + label))
		}
		b.WriteString("\n")
	}

	b.WriteString("\n")
	b.WriteString(StatusBarStyle.Render("  ↑/↓ navigate • enter select • esc back"))
	return b.String()
}

func (m SetupModel) viewLanguage() string {
	var b strings.Builder
	b.WriteString("  Select default language:\n\n")

	for i, lang := range m.languages {
		cursor := "  "
		if i == m.langCursor {
			cursor = "▸ "
		}
		label := lang.name
		if lang.code != "" {
			label = fmt.Sprintf("%s (%s)", lang.name, lang.code)
		}
		if i == m.langCursor {
			b.WriteString(ActiveItemStyle.Render(cursor + label))
		} else {
			b.WriteString(NormalItemStyle.Render(cursor + label))
		}
		b.WriteString("\n")
	}

	b.WriteString("\n")
	b.WriteString(StatusBarStyle.Render("  ↑/↓ navigate • enter select • esc back"))
	return b.String()
}

func (m SetupModel) viewHotkey() string {
	var b strings.Builder
	b.WriteString("  Set dictation hotkey:\n")
	b.WriteString("  " + lipgloss.NewStyle().Foreground(ColorDimFg).Render("Select a hotkey for push-to-talk dictation") + "\n\n")

	for i, preset := range m.hotkeyPresets {
		cursor := "  "
		if i == m.hotkeyCursor {
			cursor = "▸ "
		}
		label := preset.label
		if i == m.hotkeyCursor {
			b.WriteString(ActiveItemStyle.Render(cursor + label))
		} else {
			b.WriteString(NormalItemStyle.Render(cursor + label))
		}
		b.WriteString("\n")
	}

	if m.hotkeyCustom {
		b.WriteString("\n")
		input := m.hotkeyInput
		if input == "" {
			input = "type hotkey combo…"
		}
		b.WriteString(lipgloss.NewStyle().
			Foreground(ColorPrimary).
			Bold(true).
			Border(lipgloss.RoundedBorder()).
			BorderForeground(ColorPrimary).
			Padding(0, 2).
			MarginLeft(2).
			Render("▎ " + input))
	}

	b.WriteString("\n\n")
	if m.hotkeyCustom {
		b.WriteString(StatusBarStyle.Render("  Type hotkey string • backspace to delete • enter to confirm • esc back"))
	} else {
		b.WriteString(StatusBarStyle.Render("  ↑/↓ navigate • enter select • esc back"))
	}
	return b.String()
}

func (m SetupModel) viewGRPC() string {
	var b strings.Builder
	b.WriteString("  gRPC Server:\n")
	b.WriteString("  " + lipgloss.NewStyle().Foreground(ColorDimFg).Render("Enable gRPC server alongside dictation for gleann integration") + "\n\n")

	// Toggle display.
	enabledStr := "○ Disabled"
	enabledStyle := CheckboxUnchecked
	if m.grpcEnabled {
		enabledStr = "● Enabled"
		enabledStyle = CheckboxChecked
	}
	b.WriteString(ActiveItemStyle.Render(fmt.Sprintf("  ▸ %s", enabledStyle.Render(enabledStr))))
	b.WriteString("\n")

	// Address display.
	if m.grpcEnabled {
		addrLabel := m.grpcAddr
		if addrLabel == "" {
			addrLabel = "localhost:50051"
		}
		b.WriteString("\n")
		if m.grpcEditing {
			b.WriteString(lipgloss.NewStyle().
				Foreground(ColorPrimary).
				Bold(true).
				Border(lipgloss.RoundedBorder()).
				BorderForeground(ColorPrimary).
				Padding(0, 2).
				MarginLeft(2).
				Render("▎ " + m.grpcAddr))
		} else {
			b.WriteString(NormalItemStyle.Render(fmt.Sprintf("    Address: %s", addrLabel)))
		}
		b.WriteString("\n")
	}

	b.WriteString("\n")
	if m.grpcEditing {
		b.WriteString(StatusBarStyle.Render("  Type address • backspace to delete • enter to confirm • esc back"))
	} else {
		hints := "  space/↑/↓ toggle"
		if m.grpcEnabled {
			hints += " • e edit address"
		}
		hints += " • enter continue • esc back"
		b.WriteString(StatusBarStyle.Render(hints))
	}
	return b.String()
}

func (m SetupModel) viewBackend() string {
	var b strings.Builder
	b.WriteString("  Select transcription backend:\n")
	b.WriteString("  " + lipgloss.NewStyle().Foreground(ColorDimFg).Render("whisper.cpp (CPU) or ONNX Runtime (CPU/GPU)") + "\n\n")

	descriptions := map[string]string{
		"whisper":  "whisper.cpp — CPU-only, low latency, no extra deps",
		"onnx":     "ONNX Runtime — CPU/GPU, requires libonnxruntime",
	}

	for i, opt := range m.backendOptions {
		cursor := "  "
		if i == m.backendCursor {
			cursor = "▸ "
		}
		label := descriptions[opt]
		if label == "" {
			label = opt
		}
		if i == m.backendCursor {
			b.WriteString(ActiveItemStyle.Render(cursor + label))
		} else {
			b.WriteString(NormalItemStyle.Render(cursor + label))
		}
		b.WriteString("\n")
	}

	b.WriteString("\n")
	b.WriteString(StatusBarStyle.Render("  ↑/↓ navigate • enter select • esc back"))
	return b.String()
}

func (m SetupModel) viewOutput() string {
	var b strings.Builder
	b.WriteString("  Output Directory:\n")
	b.WriteString("  " + lipgloss.NewStyle().Foreground(ColorDimFg).Render("Where transcription files (JSONL) are saved") + "\n\n")

	if m.outputEditing {
		b.WriteString(lipgloss.NewStyle().
			Foreground(ColorPrimary).
			Bold(true).
			Border(lipgloss.RoundedBorder()).
			BorderForeground(ColorPrimary).
			Padding(0, 2).
			MarginLeft(2).
			Render("▎ " + m.outputDir))
	} else {
		b.WriteString(NormalItemStyle.Render(fmt.Sprintf("    Path: %s", m.outputDir)))
	}
	b.WriteString("\n")

	b.WriteString("\n")
	if m.outputEditing {
		b.WriteString(StatusBarStyle.Render("  Type path • backspace to delete • enter to confirm • esc back"))
	} else {
		b.WriteString(StatusBarStyle.Render("  e edit path • enter continue • esc back"))
	}
	return b.String()
}

func (m SetupModel) viewAudioSource() string {
	var b strings.Builder
	b.WriteString("  Select audio source for listen mode:\n")
	b.WriteString("  " + lipgloss.NewStyle().Foreground(ColorDimFg).Render("Choose what audio to capture for live transcription") + "\n\n")

	descriptions := map[string]string{
		"mic":     "Microphone — Default input device (voice)",
		"speaker": "Speaker — System audio / loopback (desktop audio)",
		"both":    "Both — Microphone + Speaker simultaneously",
	}

	hints := map[string]string{
		"mic":     "",
		"speaker": " (Linux: PulseAudio monitor, Windows: WASAPI loopback)",
		"both":    " (mixes mic + system audio into one stream)",
	}

	for i, opt := range m.audioSourceOptions {
		cursor := "  "
		if i == m.audioSourceCursor {
			cursor = "▸ "
		}
		label := descriptions[opt]
		if label == "" {
			label = opt
		}
		line := cursor + label
		if i == m.audioSourceCursor && hints[opt] != "" {
			line += lipgloss.NewStyle().Foreground(ColorDimFg).Render(hints[opt])
		}
		if i == m.audioSourceCursor {
			b.WriteString(ActiveItemStyle.Render(line))
		} else {
			b.WriteString(NormalItemStyle.Render(line))
		}
		b.WriteString("\n")
	}

	b.WriteString("\n")
	b.WriteString(StatusBarStyle.Render("  ↑/↓ navigate • enter select • esc back"))
	return b.String()
}

func (m SetupModel) viewSummary() string {
	var b strings.Builder
	b.WriteString("  Configuration Summary:\n\n")

	defaultModel := "(none)"
	if m.defaultCursor < len(m.installedModels) {
		defaultModel = m.installedModels[m.defaultCursor].Name
	}

	lang := "auto-detect"
	if m.langCursor < len(m.languages) && m.languages[m.langCursor].code != "" {
		lang = m.languages[m.langCursor].name
	}

	hotkey := m.selectedHotkey()

	grpcStatus := "disabled"
	if m.grpcEnabled && m.grpcAddr != "" {
		grpcStatus = m.grpcAddr
	}

	backend := m.backendOptions[m.backendCursor]
	audioSource := m.audioSourceOptions[m.audioSourceCursor]
	outputDir := m.outputDir
	if outputDir == "" {
		outputDir = "(not set)"
	}

	items := []struct{ k, v string }{
		{"Default Model", defaultModel},
		{"Language", lang},
		{"Dictation Hotkey", hotkey},
		{"Backend", backend},
		{"Audio Source", audioSource},
		{"Output Directory", outputDir},
		{"gRPC Server", grpcStatus},
		{"Models Installed", fmt.Sprintf("%d", len(m.installedModels))},
		{"Config Path", config.ConfigPath()},
	}

	for _, item := range items {
		b.WriteString(fmt.Sprintf("  %s  %s\n",
			lipgloss.NewStyle().Foreground(ColorAccent).Bold(true).Width(20).Render(item.k),
			lipgloss.NewStyle().Foreground(ColorFg).Render(item.v),
		))
	}

	b.WriteString("\n")
	b.WriteString(SuccessBadge.Render("  Press enter/y to save, n to cancel"))
	b.WriteString("\n")
	b.WriteString(StatusBarStyle.Render("  Config will be saved to " + config.ConfigPath()))
	return b.String()
}
