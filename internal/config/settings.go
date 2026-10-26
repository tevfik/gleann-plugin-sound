package config

import (
	"fmt"
	"os"
	"regexp"
	"strings"

	"github.com/tevfik/gleann/modules/settings"
)

// DefaultSpeakerThreshold mirrors speaker.DefaultThreshold (a test keeps them
// equal; this package cannot import speaker).
const DefaultSpeakerThreshold = 0.35

var (
	langRe   = regexp.MustCompile(`^[a-z]{2,3}$`)
	hotkeyRe = regexp.MustCompile(`^[a-z0-9_]+(\+[a-z0-9_]+)*$`)
)

// Settings lists every user-editable setting once. The web form (GET/PUT
// /settings), the TUI menus and their validation all come from it, so a new
// setting added here appears everywhere.
func Settings() *settings.Registry[Config] {
	return &settings.Registry[Config]{
		Title:      "gleann-plugin-sound",
		ModelsPath: "/models",
		Note:       "Saved to ~/.gleann/sound.json. Restart plugin-serve and the dictate service to apply.",
		Load: func() (*Config, error) {
			if c := Load(); c != nil {
				return c, nil
			}
			return &Config{}, nil
		},
		Save: Save,
		Internal: []string{
			// install state and lists the setup wizard and 'models' manage
			"models", "install_path", "completions_installed", "input_group_setup",
			"onnx_runtime_system_wide", "daemon_enabled", "completed",
			// a word map, edited in sound.json (README, "Corrections")
			"corrections",
		},
		Fields: []settings.Field[Config]{
			{Key: "language", Label: "Language", Type: settings.Select, Group: "Recognition", AllowCustom: true,
				Help: "Language of the speech. Auto-detect is slower and less accurate for streaming.",
				Options: settings.Opts("", "Auto-detect", "tr", "Türkçe", "en", "English", "de", "Deutsch", "fr", "Français",
					"es", "Español", "it", "Italiano", "pt", "Português", "nl", "Nederlands", "ru", "Русский",
					"ar", "العربية", "ja", "日本語", "zh", "中文"),
				Bind: func(c *Config) any { return &c.Language },
				Check: func(_ *Config, v any) (any, error) {
					if s := v.(string); s != "" && !langRe.MatchString(s) {
						return nil, fmt.Errorf("must be empty (auto) or a language code like tr, en")
					}
					return v, nil
				}},
			{Key: "backend", Label: "Backend", Type: settings.Select, Group: "Recognition", Default: "whisper",
				Help:    "tc is the most accurate live backend (needs gleann-asr-helper); whisper and onnx are offline.",
				Options: settings.Opts("whisper", "whisper.cpp", "onnx", "Whisper on ONNX Runtime", "nemotron", "Nemotron int4 (ONNX)", "tc", "Nemotron Q8 (GGUF, via helper)"),
				Bind:    func(c *Config) any { return &c.Backend }},
			{Key: "default_model", Label: "Model", Type: settings.Select, Group: "Recognition", AllowCustom: true,
				Help:        "A downloaded model, or Custom… for a file or directory elsewhere. Download more from Setup -> Models & Downloads.",
				OptionsFrom: func(c *Config) ([]settings.Option, error) { return installedModels(c, false), nil },
				Bind:        func(c *Config) any { return &c.DefaultModel },
				Check: func(_ *Config, v any) (any, error) {
					if v == "" {
						return nil, fmt.Errorf("a model is required")
					}
					return existingPath(v.(string))
				}},
			{Key: "refine_model", Label: "Refine model", Type: settings.Select, Group: "Recognition", AllowCustom: true,
				Help:        "Qwen3-ASR GGUF that re-transcribes finished utterances more accurately in the background (live backends only).",
				Options:     settings.Opts("", "Off"),
				OptionsFrom: func(c *Config) ([]settings.Option, error) { return installedModels(c, true), nil },
				Bind:        func(c *Config) any { return &c.RefineModel },
				Check:       func(_ *Config, v any) (any, error) { return existingPath(v.(string)) }},
			{Key: "execution_provider", Label: "ONNX execution provider", Type: settings.Select, Group: "Recognition", Default: "auto",
				Options: settings.Opts("auto", "Auto (CUDA when available)", "cuda", "CUDA", "cpu", "CPU"),
				Bind:    func(c *Config) any { return &c.ExecutionProvider }},
			{Key: "audio_source", Label: "Audio source", Type: settings.Select, Group: "Capture", Default: "mic",
				Help:    "What 'listen' captures: the microphone, the speaker output (meetings played on this machine) or both.",
				Options: settings.Opts("mic", "Microphone", "speaker", "Speaker output (loopback)", "both", "Both"),
				Bind:    func(c *Config) any { return &c.AudioSource }},
			{Key: "hotkey", Label: "Dictation hotkey", Type: settings.Text, Group: "Dictation",
				Help: "Push-to-talk key combination, e.g. ctrl+shift+space.",
				Bind: func(c *Config) any { return &c.Hotkey },
				Check: func(_ *Config, v any) (any, error) {
					s := strings.ToLower(v.(string))
					if s != "" && !hotkeyRe.MatchString(s) {
						return nil, fmt.Errorf("use keys joined by +, e.g. ctrl+shift+space")
					}
					return s, nil
				}},
			{Key: "output_dir", Label: "Output directory", Type: settings.Path, Group: "Output",
				Help: "Where 'transcribe' and 'listen' write results.", Bind: func(c *Config) any { return &c.OutputDir }},
			{Key: "speaker_model", Label: "Speaker model", Type: settings.Path, Group: "Speakers",
				Help:  "TitaNet ONNX file that tells voices apart. Empty = ~/.gleann/models/nemo_en_titanet_small.onnx.",
				Bind:  func(c *Config) any { return &c.SpeakerModel },
				Check: func(_ *Config, v any) (any, error) { return existingPath(v.(string)) }},
			{Key: "speaker_threshold", Label: "Speaker match threshold", Type: settings.Number, Group: "Speakers",
				Help:    "Cosine an utterance must reach to be named after an enrolled voice in transcripts. Higher = stricter, more '?'.",
				Default: DefaultSpeakerThreshold, Min: settings.F(0.20), Max: settings.F(0.95), Step: settings.F(0.05),
				Bind: func(c *Config) any { return &c.SpeakerThreshold }},
			{Key: "dictate_speaker_match", Label: "Dictate only my voice", Type: settings.Bool, Group: "Speakers",
				Help: "Type only speech that matches an enrolled voice. Words then appear when you pause.",
				Bind: func(c *Config) any { return &c.DictateSpeakerMatch }},
			{Key: "dictate_speaker_threshold", Label: "Voice threshold", Type: settings.Number, Group: "Speakers",
				Help:    "How closely speech must match your voice to be typed. Higher rejects other voices more, but may drop yours.",
				Default: DefaultDictateSpeakerThreshold, Min: settings.F(0.30), Max: settings.F(0.80), Step: settings.F(0.05),
				VisibleIf: &settings.Cond{Key: "dictate_speaker_match", Equals: true},
				Bind:      func(c *Config) any { return &c.DictateSpeakerThreshold }},
		},
	}
}

// installedModels lists the downloaded models: the refiners, or the others.
func installedModels(c *Config, refiners bool) []settings.Option {
	var opts []settings.Option
	for _, m := range c.Models {
		if IsRefineModel(m.Name) == refiners {
			opts = append(opts, settings.Option{Value: m.Path, Label: fmt.Sprintf("%s (%s)", m.Name, m.Size)})
		}
	}
	return opts
}

// existingPath accepts "" (unset) or a path that exists, expanding ~.
func existingPath(v string) (any, error) {
	if v == "" {
		return "", nil
	}
	p := ExpandPath(v)
	if _, err := os.Stat(p); err != nil {
		return nil, fmt.Errorf("%s does not exist", p)
	}
	return p, nil
}
