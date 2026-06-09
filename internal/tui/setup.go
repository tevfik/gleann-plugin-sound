package tui

import (
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/charmbracelet/bubbles/spinner"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/tevfik/gleann-plugin-sound/internal/config"
)

// ── Wizard phases ──────────────────────────────────────────────
//
// Onboarding order:
//   models → download → backend → exec provider → default model →
//   language → hotkey → gRPC → output → audio source → summary
//
// Reconfiguration (cfg.Completed):
//   overview → (jump to any phase) → overview

type setupPhase int

const (
	phaseOverview          setupPhase = iota // settings overview (reconfigure mode)
	phaseModelSelect                         // select models to download
	phaseDownloading                         // downloading models
	phaseBackend                             // backend selection (whisper / onnx)
	phaseExecutionProvider                   // ONNX execution provider (auto / cuda / cpu)
	phasePipInstall                          // pip install onnxruntime-gpu prompt
	phaseDefaultModel                        // choose default model
	phaseLanguage                            // language selection
	phaseHotkey                              // hotkey configuration
	phaseGRPC                                // gRPC server configuration
	phaseOutput                              // output directory for transcriptions
	phaseAudioSource                         // audio source selection (mic / speaker / both)
	phaseSummary                             // summary & confirm
)

// onboardingOrder defines the sequential phase order for first-time setup.
// Used by nextPhase/prevPhase to navigate the wizard.
var onboardingOrder = []setupPhase{
	phaseModelSelect,
	phaseDownloading,
	phaseBackend,
	phaseExecutionProvider,
	phasePipInstall,
	phaseDefaultModel,
	phaseLanguage,
	phaseHotkey,
	phaseGRPC,
	phaseOutput,
	phaseAudioSource,
	phaseSummary,
}

// ── Messages ───────────────────────────────────────────────────

type downloadDoneMsg struct {
	model config.WhisperModel
	err   error
}

type onnxRTDownloadDoneMsg struct {
	err error
}

type pipInstallDoneMsg struct {
	libPath string
	err     error
}

// ── Overview items ─────────────────────────────────────────────

type overviewItem struct {
	label string
	phase setupPhase // which phase to jump to
}

var overviewItems = []overviewItem{
	{"Models & Downloads", phaseModelSelect},
	{"Backend", phaseBackend},
	{"Execution Provider", phaseExecutionProvider},
	{"Default Model", phaseDefaultModel},
	{"Language", phaseLanguage},
	{"Dictation Hotkey", phaseHotkey},
	{"gRPC Server", phaseGRPC},
	{"Output Directory", phaseOutput},
	{"Audio Source", phaseAudioSource},
	{"Save & Exit", phaseSummary},
}

// ── SetupModel ─────────────────────────────────────────────────

type SetupModel struct {
	phase     setupPhase
	width     int
	height    int
	cancelled bool
	done      bool

	// Overview mode (reconfiguration).
	isReconfigure  bool       // true when cfg.Completed was true on entry
	returnPhase    setupPhase // phase to return to after editing (-1 = follow wizard)
	overviewCursor int        // cursor position in overview list

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
	languages  []langOption
	langCursor int

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

	// Execution provider (ONNX).
	providerOptions []string // available providers (auto, cuda, cpu)
	providerCursor  int      // cursor position

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
	// Append ONNX Runtime models.
	available = append(available, config.AvailableONNXModels()...)
	// Append Nemotron ASR models.
	available = append(available, config.AvailableNemotronModels()...)
	// Append Silero VAD as a downloadable model option.
	available = append(available, config.SileroVADModel())

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
	backendCursor := 0  // 0 = whisper
	providerCursor := 0 // 0 = auto
	outputDir := "~/.gleann/transcriptions"
	audioSourceCursor := 0 // 0 = mic
	isReconfigure := false
	if existingCfg != nil {
		existingDefault = existingCfg.DefaultModel
		if existingCfg.GRPCAddr != "" {
			grpcEnabled = true
			grpcAddr = existingCfg.GRPCAddr
		}
		if existingCfg.Backend == "onnx" {
			backendCursor = 1
		} else if existingCfg.Backend == "nemotron" {
			backendCursor = 2
		}
		switch existingCfg.ExecutionProvider {
		case "cuda":
			providerCursor = 1
		case "cpu":
			providerCursor = 2
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
		isReconfigure = existingCfg.Completed

	}

	startPhase := setupPhase(phaseModelSelect)
	if isReconfigure {
		startPhase = phaseOverview
	}

	m := SetupModel{
		phase:                startPhase,
		isReconfigure:        isReconfigure,
		returnPhase:          -1,
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
		backendOptions:       []string{"whisper", "onnx", "nemotron"},
		backendCursor:        backendCursor,
		providerOptions:      []string{"auto", "cuda", "cpu"},
		providerCursor:       providerCursor,
		outputDir:            outputDir,
		audioSourceOptions:   []string{"mic", "speaker", "both"},
		audioSourceCursor:    audioSourceCursor,
	}

	// In reconfigure mode, pre-populate installed models from config.
	if isReconfigure && existingCfg != nil && len(existingCfg.Models) > 0 {
		m.installedModels = existingCfg.Models
		m.prefillDefaultCursor()
	}

	return m
}

func (m SetupModel) Init() tea.Cmd {
	return m.spinner.Tick
}

func (m SetupModel) Cancelled() bool         { return m.cancelled }
func (m SetupModel) Done() bool              { return m.done }
func (m SetupModel) Result() *config.Config  { return m.result }

// ── Phase navigation helpers ──────────────────────────────────

// finishPhase is called when a phase's "enter" action completes.
// In overview mode it returns to the overview; in onboarding mode it advances.
func (m *SetupModel) finishPhase() {
	if m.returnPhase == phaseOverview {
		m.phase = phaseOverview
		m.returnPhase = -1
		return
	}
	// Onboarding: advance to next phase in sequence.
	m.advanceOnboarding()
}

// advanceOnboarding moves to the next phase in onboarding order,
// skipping phases that don't apply.
func (m *SetupModel) advanceOnboarding() {
	idx := -1
	for i, p := range onboardingOrder {
		if p == m.phase {
			idx = i
			break
		}
	}
	if idx < 0 || idx >= len(onboardingOrder)-1 {
		m.phase = phaseSummary
		return
	}
	next := onboardingOrder[idx+1]
	// Skip download phase (handled by model select).
	if next == phaseDownloading {
		if idx+2 < len(onboardingOrder) {
			next = onboardingOrder[idx+2]
		}
	}
	// Skip ONNX-only phases if backend isn't onnx or nemotron.
	backend := m.backendOptions[m.backendCursor]
	isONNX := backend == "onnx" || backend == "nemotron"
	for !isONNX && (next == phaseExecutionProvider || next == phasePipInstall) {
		idx++
		if idx >= len(onboardingOrder)-1 {
			next = phaseSummary
			break
		}
		next = onboardingOrder[idx+1]
	}
	// Skip pip install if it doesn't apply (macOS, already installed, etc.).
	if next == phasePipInstall && !needsPipInstall() {
		idx++
		if idx >= len(onboardingOrder)-1 {
			next = phaseSummary
		} else {
			next = onboardingOrder[idx+1]
		}
	}
	m.phase = next
}

// goBackOnboarding moves to the previous phase in onboarding order.
func (m *SetupModel) goBackOnboarding() bool {
	if m.returnPhase == phaseOverview {
		m.phase = phaseOverview
		m.returnPhase = -1
		return true
	}
	idx := -1
	for i, p := range onboardingOrder {
		if p == m.phase {
			idx = i
			break
		}
	}
	if idx <= 0 {
		return false // at the start
	}
	prev := onboardingOrder[idx-1]
	// Skip download phase.
	if prev == phaseDownloading && idx >= 2 {
		prev = onboardingOrder[idx-2]
	}
	// Skip pip install going backwards (user already decided or N/A).
	if prev == phasePipInstall {
		idx--
		if idx >= 1 {
			prev = onboardingOrder[idx-1]
		}
	}
	// Skip execution provider if backend isn't onnx or nemotron.
	backend := m.backendOptions[m.backendCursor]
	isONNX := backend == "onnx" || backend == "nemotron"
	if !isONNX && prev == phaseExecutionProvider {
		for j := idx - 1; j >= 0; j-- {
			if onboardingOrder[j] != phaseExecutionProvider && onboardingOrder[j] != phaseDownloading && onboardingOrder[j] != phasePipInstall {
				prev = onboardingOrder[j]
				break
			}
		}
	}
	m.phase = prev
	return true
}

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
			// Only add transcription models to installedModels (not VAD or other utilities).
			if msg.model.Name != "silero-vad" {
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
		// If ONNX or Nemotron backend is selected and runtime not installed, download it now.
		backend := m.backendOptions[m.backendCursor]
		if m.backendCursor < len(m.backendOptions) && (backend == "onnx" || backend == "nemotron") && !config.IsONNXRuntimeDownloaded() {
			m.downloading = fmt.Sprintf("ONNX Runtime v%s", config.ONNXRuntimeVersion)
			return m, func() tea.Msg {
				_, err := config.DownloadONNXRuntime()
				return onnxRTDownloadDoneMsg{err: err}
			}
		}
		m.prefillDefaultCursor()
		// After download, go to backend selection (onboarding) or overview (reconfig).
		if m.returnPhase == phaseOverview {
			m.phase = phaseOverview
			m.returnPhase = -1
		} else {
			m.phase = phaseBackend
		}
		return m, nil

	case onnxRTDownloadDoneMsg:
		if msg.err != nil {
			m.downloadErr = fmt.Sprintf("Failed to download ONNX Runtime: %v", msg.err)
		} else {
			m.downloaded = append(m.downloaded, fmt.Sprintf("ONNX Runtime v%s", config.ONNXRuntimeVersion))
		}
		m.prefillDefaultCursor()
		if m.returnPhase == phaseOverview {
			m.phase = phaseOverview
			m.returnPhase = -1
		} else {
			m.phase = phaseBackend
		}
		return m, nil

	case pipInstallDoneMsg:
		if msg.err != nil {
			m.downloadErr = fmt.Sprintf("pip install failed: %v", msg.err)
		} else {
			m.downloaded = append(m.downloaded, "onnxruntime-gpu (pip)")
		}
		m.downloading = ""
		m.finishPhase()
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
			// In text editing modes, esc exits editing.
			if m.phase == phaseGRPC && m.grpcEditing {
				m.grpcEditing = false
				return m, nil
			}
			if m.phase == phaseOutput && m.outputEditing {
				m.outputEditing = false
				return m, nil
			}
			// Overview: esc quits.
			if m.phase == phaseOverview {
				m.cancelled = true
				return m, tea.Quit
			}
			// Go back.
			if m.goBackOnboarding() {
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
	case phaseOverview:
		return m.updateOverview(msg)
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
	case phaseExecutionProvider:
		return m.updateExecutionProvider(msg)
	case phasePipInstall:
		return m.updatePipInstall(msg)
	case phaseOutput:
		return m.updateOutput(msg)
	case phaseAudioSource:
		return m.updateAudioSource(msg)
	case phaseSummary:
		return m.updateSummary(msg)
	}
	return m, nil
}

// ── Overview (reconfigure mode) ────────────────────────────────

func (m SetupModel) updateOverview(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "up", "k":
		if m.overviewCursor > 0 {
			m.overviewCursor--
		}
	case "down", "j":
		if m.overviewCursor < len(overviewItems)-1 {
			m.overviewCursor++
		}
	case "enter":
		item := overviewItems[m.overviewCursor]
		if item.phase == phaseSummary {
			// "Save & Exit" — go directly to save.
			m.done = true
			m.result = m.buildConfig()
			return m, tea.Quit
		}
		m.returnPhase = phaseOverview
		m.phase = item.phase
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
			// All selected models already downloaded, skip to next.
			m.addPreExistingModels()
			if len(m.installedModels) == 0 {
				// Nothing selected at all.
				return m, nil
			}
			m.prefillDefaultCursor()
			m.finishPhase()
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
		// Skip non-transcription models (VAD etc).
		if m.available[i].Name == "silero-vad" {
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
	filtered := m.filteredInstalledModels()

	// No matching models — redirect to model download on any key.
	if len(filtered) == 0 {
		if msg.String() == "enter" || msg.String() == "esc" {
			m.returnPhase = phaseDefaultModel
			m.phase = phaseModelSelect
		}
		return m, nil
	}

	switch msg.String() {
	case "up", "k":
		if m.defaultCursor > 0 {
			m.defaultCursor--
		}
	case "down", "j":
		if m.defaultCursor < len(filtered)-1 {
			m.defaultCursor++
		}
	case "enter":
		// Store the selected model from filtered list back to the actual index.
		if m.defaultCursor < len(filtered) {
			selected := filtered[m.defaultCursor]
			// Find actual index in full list so buildConfig uses the right entry.
			for i, e := range m.installedModels {
				if e.Name == selected.Name && e.Path == selected.Path {
					m.defaultCursor = i
					break
				}
			}
		}
		m.finishPhase()
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
		m.finishPhase()
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
				m.finishPhase()
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
			m.finishPhase()
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
			}
		case "backspace":
			if len(m.grpcAddr) > 0 {
				m.grpcAddr = m.grpcAddr[:len(m.grpcAddr)-1]
			}
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
		m.finishPhase()
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
		// Reset default model cursor when backend changes — the filtered
		// list will be different so the old cursor position is invalid.
		m.defaultCursor = 0

		// In onboarding: onnx/nemotron → exec provider, whisper → skip to default model.
		// In overview: onnx/nemotron → exec provider, whisper → back to overview.
		backend := m.backendOptions[m.backendCursor]
		if backend == "onnx" || backend == "nemotron" {
			if m.returnPhase == phaseOverview {
				// Stay in overview flow but visit exec provider first.
				m.phase = phaseExecutionProvider
			} else {
				m.phase = phaseExecutionProvider
			}
		} else {
			m.finishPhase()
		}
	}
	return m, nil
}

// ── Execution Provider ────────────────────────────────────────

func (m SetupModel) updateExecutionProvider(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "up", "k":
		if m.providerCursor > 0 {
			m.providerCursor--
		}
	case "down", "j":
		if m.providerCursor < len(m.providerOptions)-1 {
			m.providerCursor++
		}
	case "enter":
		provider := m.providerOptions[m.providerCursor]
		// If CUDA or auto is selected and we need GPU runtime, go to pip install phase.
		if (provider == "cuda" || provider == "auto") && needsPipInstall() {
			m.phase = phasePipInstall
			return m, nil
		}
		m.finishPhase()
	}
	return m, nil
}

// needsPipInstall checks if the pip install phase should be shown.
// Returns true when: not macOS, GPU pip package exists, pip is available,
// and no CUDA-capable library is already installed.
func needsPipInstall() bool {
	// macOS uses CoreML from base package — no separate GPU package.
	if runtime.GOOS == "darwin" {
		return false
	}
	// No GPU pip package for this platform.
	if config.PipPackageForGPU() == "" {
		return false
	}
	// Already have a CUDA-capable pip install.
	if config.HasPipInstalledCUDAProvider() {
		return false
	}
	return true
}

// ── Pip Install (GPU ONNX Runtime) ────────────────────────────

func (m SetupModel) updatePipInstall(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "y", "Y", "enter":
		pipBin := config.FindPip()
		if pipBin == "" {
			m.downloadErr = fmt.Sprintf("pip not found. Install manually: pip install %s", config.PipPackageForGPU())
			m.finishPhase()
			return m, nil
		}
		m.downloading = config.PipPackageForGPU()
		m.phase = phaseDownloading
		return m, func() tea.Msg {
			libPath, err := config.InstallONNXRuntimeGPUViaPip()
			return pipInstallDoneMsg{libPath: libPath, err: err}
		}
	case "n", "N", "esc":
		// Skip — continue with CPU-only.
		m.finishPhase()
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
			}
		case "backspace":
			if len(m.outputDir) > 0 {
				m.outputDir = m.outputDir[:len(m.outputDir)-1]
			}
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
		m.finishPhase()
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
		m.finishPhase()
	}
	return m, nil
}

// filteredInstalledModels returns only models matching the current backend.
func (m SetupModel) filteredInstalledModels() []config.ModelEntry {
	backend := m.backendOptions[m.backendCursor]
	var filtered []config.ModelEntry
	for _, e := range m.installedModels {
		isCompatible := false
		if backend == "whisper" {
			isCompatible = !strings.HasPrefix(e.Name, "onnx-") && !strings.HasPrefix(e.Name, "nemotron-")
		} else if backend == "onnx" {
			isCompatible = strings.HasPrefix(e.Name, "onnx-")
		} else if backend == "nemotron" {
			isCompatible = strings.HasPrefix(e.Name, "nemotron-")
		}
		if isCompatible {
			filtered = append(filtered, e)
		}
	}
	return filtered
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
		candidate := m.installedModels[m.defaultCursor]
		// Validate that the selected model matches the backend.
		backend := m.backendOptions[m.backendCursor]
		isCompatible := false
		if backend == "whisper" {
			isCompatible = !strings.HasPrefix(candidate.Name, "onnx-") && !strings.HasPrefix(candidate.Name, "nemotron-")
		} else if backend == "onnx" {
			isCompatible = strings.HasPrefix(candidate.Name, "onnx-")
		} else if backend == "nemotron" {
			isCompatible = strings.HasPrefix(candidate.Name, "nemotron-")
		}
		if isCompatible {
			defaultModel = candidate.Path
		} else {
			// Mismatch — pick the first compatible model instead.
			for _, e := range m.installedModels {
				compatible := false
				if backend == "whisper" {
					compatible = !strings.HasPrefix(e.Name, "onnx-") && !strings.HasPrefix(e.Name, "nemotron-")
				} else if backend == "onnx" {
					compatible = strings.HasPrefix(e.Name, "onnx-")
				} else if backend == "nemotron" {
					compatible = strings.HasPrefix(e.Name, "nemotron-")
				}
				if compatible {
					defaultModel = e.Path
					break
				}
			}
		}
	}

	lang := ""
	if m.langCursor < len(m.languages) {
		lang = m.languages[m.langCursor].code
	}

	hotkey := m.selectedHotkey()

	backend := m.backendOptions[m.backendCursor]

	cfg := &config.Config{
		DefaultModel: defaultModel,
		Language:      lang,
		Hotkey:        hotkey,
		Models:        m.installedModels,
		Backend:       backend,
		AudioSource:   m.audioSourceOptions[m.audioSourceCursor],
		OutputDir:     m.outputDir,
		Completed:     true,
	}
	if backend == "onnx" || backend == "nemotron" {
		cfg.ExecutionProvider = m.providerOptions[m.providerCursor]
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

		// Multi-file bundle (ONNX models).
		if model.IsBundle() {
			return downloadBundle(model)
		}

		// Single-file download (GGML, Silero VAD).
		return downloadSingleFile(model)
	}
}

func downloadSingleFile(model config.WhisperModel) tea.Msg {
	dest := config.ModelPath(model.FileName)

	// Skip if already exists.
	if _, err := os.Stat(dest); err == nil {
		return downloadDoneMsg{model: model, err: nil}
	}

	if err := httpDownload(model.URL, dest); err != nil {
		return downloadDoneMsg{model: model, err: err}
	}
	return downloadDoneMsg{model: model, err: nil}
}

func downloadBundle(model config.WhisperModel) tea.Msg {
	bundleDir := config.ModelPath(model.FileName)
	if err := os.MkdirAll(bundleDir, 0o755); err != nil {
		return downloadDoneMsg{model: model, err: err}
	}

	for _, bf := range model.BundleFiles {
		dest := filepath.Join(bundleDir, bf.Name)
		// Skip files already downloaded.
		if _, err := os.Stat(dest); err == nil {
			continue
		}
		if err := httpDownload(bf.URL, dest); err != nil {
			return downloadDoneMsg{model: model, err: fmt.Errorf("%s: %w", bf.Name, err)}
		}
	}
	return downloadDoneMsg{model: model, err: nil}
}

// httpDownload fetches a URL and writes it to dest atomically via a .tmp file.
func httpDownload(url, dest string) error {
	resp, err := http.Get(url)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("HTTP %d for %s", resp.StatusCode, url)
	}

	f, err := os.Create(dest + ".tmp")
	if err != nil {
		return err
	}

	if _, err := io.Copy(f, resp.Body); err != nil {
		f.Close()
		os.Remove(dest + ".tmp")
		return err
	}
	f.Close()

	return os.Rename(dest+".tmp", dest)
}

// ── View ───────────────────────────────────────────────────────

func (m SetupModel) View() string {
	var b strings.Builder
	b.WriteString(TitleStyle.Render(" gleann-plugin-sound Setup "))
	b.WriteString("\n\n")

	switch m.phase {
	case phaseOverview:
		b.WriteString(m.viewOverview())
	case phaseModelSelect:
		b.WriteString(m.viewModelSelect())
	case phaseDownloading:
		b.WriteString(m.viewDownloading())
	case phaseBackend:
		b.WriteString(m.viewBackend())
	case phaseExecutionProvider:
		b.WriteString(m.viewExecutionProvider())
	case phasePipInstall:
		b.WriteString(m.viewPipInstall())
	case phaseDefaultModel:
		b.WriteString(m.viewDefaultModel())
	case phaseLanguage:
		b.WriteString(m.viewLanguage())
	case phaseHotkey:
		b.WriteString(m.viewHotkey())
	case phaseGRPC:
		b.WriteString(m.viewGRPC())
	case phaseOutput:
		b.WriteString(m.viewOutput())
	case phaseAudioSource:
		b.WriteString(m.viewAudioSource())
	case phaseSummary:
		b.WriteString(m.viewSummary())
	}

	return b.String()
}

// ── Overview view ─────────────────────────────────────────────

func (m SetupModel) viewOverview() string {
	var b strings.Builder
	b.WriteString("  Settings:\n")
	b.WriteString("  " + lipgloss.NewStyle().Foreground(ColorDimFg).Render("Select a setting to change, or Save & Exit") + "\n\n")

	// Current values for display.
	values := m.overviewValues()

	for i, item := range overviewItems {
		cursor := "  "
		if i == m.overviewCursor {
			cursor = "▸ "
		}

		// Skip execution provider row when backend isn't onnx or nemotron.
		backend := m.backendOptions[m.backendCursor]
		if item.phase == phaseExecutionProvider && backend != "onnx" && backend != "nemotron" {
			continue
		}

		if item.phase == phaseSummary {
			// Save & Exit gets special styling.
			label := cursor + item.label
			if i == m.overviewCursor {
				b.WriteString(SuccessBadge.Render("  " + label))
			} else {
				b.WriteString(NormalItemStyle.Render(label))
			}
			b.WriteString("\n")
			continue
		}

		val := values[item.phase]
		label := fmt.Sprintf("%s%-20s %s", cursor, item.label, lipgloss.NewStyle().Foreground(ColorSecondary).Render(val))
		if i == m.overviewCursor {
			b.WriteString(ActiveItemStyle.Render(label))
		} else {
			b.WriteString(NormalItemStyle.Render(label))
		}
		b.WriteString("\n")
	}

	b.WriteString("\n")
	b.WriteString(StatusBarStyle.Render("  ↑/↓ navigate • enter edit • esc quit"))
	return b.String()
}

func (m SetupModel) overviewValues() map[setupPhase]string {
	vals := make(map[setupPhase]string)

	// Models
	modelNames := make([]string, 0, len(m.installedModels))
	for _, e := range m.installedModels {
		modelNames = append(modelNames, e.Name)
	}
	if len(modelNames) == 0 {
		vals[phaseModelSelect] = "(none)"
	} else if len(modelNames) > 3 {
		vals[phaseModelSelect] = fmt.Sprintf("%s +%d more", strings.Join(modelNames[:3], ", "), len(modelNames)-3)
	} else {
		vals[phaseModelSelect] = strings.Join(modelNames, ", ")
	}

	// Backend
	vals[phaseBackend] = m.backendOptions[m.backendCursor]

	// Exec provider
	vals[phaseExecutionProvider] = m.providerOptions[m.providerCursor]

	// Default model (show from filtered list matching backend)
	filtered := m.filteredInstalledModels()
	if m.defaultCursor < len(m.installedModels) {
		vals[phaseDefaultModel] = m.installedModels[m.defaultCursor].Name
	} else if len(filtered) > 0 {
		vals[phaseDefaultModel] = filtered[0].Name
	} else {
		vals[phaseDefaultModel] = "(no matching models)"
	}

	// Language
	if m.langCursor < len(m.languages) && m.languages[m.langCursor].code != "" {
		vals[phaseLanguage] = m.languages[m.langCursor].name
	} else {
		vals[phaseLanguage] = "auto-detect"
	}

	// Hotkey
	vals[phaseHotkey] = m.selectedHotkey()

	// gRPC
	if m.grpcEnabled && m.grpcAddr != "" {
		vals[phaseGRPC] = m.grpcAddr
	} else {
		vals[phaseGRPC] = "disabled"
	}

	// Output
	if m.outputDir != "" {
		vals[phaseOutput] = m.outputDir
	} else {
		vals[phaseOutput] = "(not set)"
	}

	// Audio source
	vals[phaseAudioSource] = m.audioSourceOptions[m.audioSourceCursor]

	return vals
}

func (m SetupModel) viewModelSelect() string {
	var b strings.Builder
	b.WriteString("  Select models to download:\n")
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

func (m SetupModel) viewBackend() string {
	var b strings.Builder
	b.WriteString("  Select transcription backend:\n")
	b.WriteString("  " + lipgloss.NewStyle().Foreground(ColorDimFg).Render("whisper.cpp (CPU) or ONNX Runtime (CPU/GPU)") + "\n\n")

	descriptions := map[string]string{
		"whisper": "whisper.cpp — CPU-only, low latency, no extra deps",
		"onnx":    "ONNX Runtime — CPU/GPU, requires libonnxruntime",
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

func (m SetupModel) viewExecutionProvider() string {
	var b strings.Builder
	b.WriteString("  ONNX Execution Provider:\n")
	b.WriteString("  " + lipgloss.NewStyle().Foreground(ColorDimFg).Render("Choose hardware acceleration for ONNX inference") + "\n\n")

	descriptions := map[string]string{
		"auto": "Auto — Try CUDA first, fall back to CPU",
		"cuda": "CUDA — NVIDIA GPU acceleration (requires CUDA toolkit)",
		"cpu":  "CPU — No GPU required, works everywhere",
	}

	for i, opt := range m.providerOptions {
		cursor := "  "
		if i == m.providerCursor {
			cursor = "▸ "
		}
		label := descriptions[opt]
		if label == "" {
			label = opt
		}
		if i == m.providerCursor {
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

func (m SetupModel) viewPipInstall() string {
	var b strings.Builder
	pkg := config.PipPackageForGPU()
	b.WriteString("  GPU ONNX Runtime:\n")
	b.WriteString("  " + lipgloss.NewStyle().Foreground(ColorDimFg).Render(
		"GPU acceleration requires the "+pkg+" pip package") + "\n\n")

	pipBin := config.FindPip()
	if pipBin == "" {
		b.WriteString(lipgloss.NewStyle().Foreground(ColorError).Render(
			"  pip not found on this system.") + "\n")
		b.WriteString("  Install manually:\n\n")
		b.WriteString("    pip install " + pkg + "\n\n")
		b.WriteString(StatusBarStyle.Render("  enter/n skip • esc back"))
	} else {
		b.WriteString(fmt.Sprintf("  Command: %s install --target ~/.gleann/lib/onnxrt-pip %s\n\n", pipBin, pkg))
		b.WriteString("  Install GPU-accelerated ONNX Runtime via pip?\n\n")
		b.WriteString(ActiveItemStyle.Render("  y") + " install  " + NormalItemStyle.Render("n") + " skip (CPU-only)\n\n")
		b.WriteString(StatusBarStyle.Render("  y install • n skip • esc back"))
	}
	return b.String()
}

func (m SetupModel) viewDefaultModel() string {
	var b strings.Builder
	backend := m.backendOptions[m.backendCursor]
	b.WriteString(fmt.Sprintf("  Select default model (%s):\n\n", backend))

	filtered := m.filteredInstalledModels()

	if len(filtered) == 0 {
		b.WriteString(ErrorBadge.Render(fmt.Sprintf("  No %s models installed. Download a matching model first.", backend)))
		b.WriteString("\n\n")
		b.WriteString(StatusBarStyle.Render("  esc go back to download models"))
		return b.String()
	}

	for i, model := range filtered {
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
	}
	if backend == "onnx" || backend == "nemotron" {
		items = append(items, struct{ k, v string }{"Exec Provider", m.providerOptions[m.providerCursor]})
	}
	items = append(items,
		struct{ k, v string }{"Audio Source", audioSource},
		struct{ k, v string }{"Output Directory", outputDir},
		struct{ k, v string }{"gRPC Server", grpcStatus},
		struct{ k, v string }{"Models Installed", fmt.Sprintf("%d", len(m.installedModels))},
		struct{ k, v string }{"Config Path", config.ConfigPath()},
	)

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
