package httpserver

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"regexp"
	"strings"

	"github.com/tevfik/gleann-plugin-sound/internal/config"
)

// SettingsSchemaVersion is the version of the GET/PUT /settings contract:
//
//	GET /settings -> {"schema_version":1,"title":...,"fields":[Field...]}
//	PUT /settings {"values":{key:value,...}}
//	              -> {"saved":[keys],"restart_required":bool}
//	              or 422 {"error":...,"errors":{key:message}} with nothing saved
//
// A generic client (the gleann web UI) renders the form from the fields, so a
// plugin can expose settings without shipping any UI.
const SettingsSchemaVersion = 1

type settingOption struct {
	Value string `json:"value"`
	Label string `json:"label"`
}

type settingField struct {
	Key     string          `json:"key"`
	Label   string          `json:"label"`
	Type    string          `json:"type"` // select | text | path | number
	Group   string          `json:"group,omitempty"`
	Help    string          `json:"help,omitempty"`
	Options []settingOption `json:"options,omitempty"` // select: allowed values; path: suggestions
	Value   any             `json:"value"`
	Default any             `json:"default,omitempty"`
	Min     *float64        `json:"min,omitempty"`
	Max     *float64        `json:"max,omitempty"`
}

var (
	langRe   = regexp.MustCompile(`^[a-z]{2,3}$`)
	hotkeyRe = regexp.MustCompile(`^[a-z0-9_]+(\+[a-z0-9_]+)*$`)
)

func opts(pairs ...string) []settingOption {
	var o []settingOption
	for i := 0; i+1 < len(pairs); i += 2 {
		o = append(o, settingOption{pairs[i], pairs[i+1]})
	}
	return o
}

func ptr(f float64) *float64 { return &f }

// settingsFields describes every editable setting with its current value.
func settingsFields(cfg *config.Config) []settingField {
	var models []settingOption
	for _, m := range cfg.Models {
		models = append(models, settingOption{Value: m.Path, Label: fmt.Sprintf("%s (%s)", m.Name, m.Size)})
	}
	return []settingField{
		{Key: "language", Label: "Language", Type: "select", Group: "Recognition",
			Help: "Language of the speech. Auto-detect is slower and less accurate for streaming.",
			Options: opts("", "Auto-detect", "tr", "Türkçe", "en", "English", "de", "Deutsch", "fr", "Français",
				"es", "Español", "it", "Italiano", "pt", "Português", "nl", "Nederlands", "ru", "Русский",
				"ar", "العربية", "ja", "日本語", "zh", "中文"),
			Value: cfg.Language},
		{Key: "backend", Label: "Backend", Type: "select", Group: "Recognition",
			Help:    "tc is the most accurate live backend (needs gleann-asr-helper); whisper and onnx are offline.",
			Options: opts("whisper", "whisper.cpp", "onnx", "Whisper on ONNX Runtime", "nemotron", "Nemotron int4 (ONNX)", "tc", "Nemotron Q8 (GGUF, via helper)"),
			Value:   orDefault(cfg.Backend, "whisper"), Default: "whisper"},
		{Key: "default_model", Label: "Model", Type: "path", Group: "Recognition",
			Help: "Model file or directory for the chosen backend.", Options: models, Value: cfg.DefaultModel},
		{Key: "refine_model", Label: "Refine model", Type: "path", Group: "Recognition",
			Help:    "Qwen3-ASR GGUF that re-transcribes finished utterances more accurately in the background. Empty = off.",
			Options: models, Value: cfg.RefineModel},
		{Key: "execution_provider", Label: "ONNX execution provider", Type: "select", Group: "Recognition",
			Options: opts("auto", "Auto (CUDA when available)", "cuda", "CUDA", "cpu", "CPU"),
			Value:   orDefault(cfg.ExecutionProvider, "auto"), Default: "auto"},
		{Key: "audio_source", Label: "Audio source", Type: "select", Group: "Capture",
			Help:    "What 'listen' captures: the microphone, the speaker output (meetings played on this machine) or both.",
			Options: opts("mic", "Microphone", "speaker", "Speaker output (loopback)", "both", "Both"),
			Value:   orDefault(cfg.AudioSource, "mic"), Default: "mic"},
		{Key: "hotkey", Label: "Dictation hotkey", Type: "text", Group: "Dictation",
			Help: "Push-to-talk key combination, e.g. ctrl+shift+space.", Value: cfg.Hotkey},
		{Key: "output_dir", Label: "Output directory", Type: "path", Group: "Output",
			Help: "Where 'transcribe' and 'listen' write results.", Value: cfg.OutputDir},
		{Key: "speaker_threshold", Label: "Speaker match threshold", Type: "number", Group: "Speakers",
			Help: "Cosine an utterance must reach to be named after an enrolled voice (0 = default 0.35). Higher = stricter, more '?'.",
			Min:  ptr(0), Max: ptr(0.95), Value: cfg.SpeakerThreshold, Default: 0.35},
	}
}

func orDefault(v, d string) string {
	if v == "" {
		return d
	}
	return v
}

func (s *Server) handleSettings(w http.ResponseWriter, r *http.Request) {
	if !s.authorized(r) {
		writeError(w, http.StatusUnauthorized, "invalid or missing token")
		return
	}
	switch r.Method {
	case http.MethodGet:
		cfg := config.Load()
		if cfg == nil {
			cfg = &config.Config{}
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"schema_version": SettingsSchemaVersion,
			"title":          "gleann-plugin-sound",
			"restart_note":   "Saved to ~/.gleann/sound.json. Restart plugin-serve and the dictate service to apply.",
			"fields":         settingsFields(cfg),
		})
	case http.MethodPut:
		s.putSettings(w, r)
	default:
		w.Header().Set("Allow", "GET, PUT")
		writeError(w, http.StatusMethodNotAllowed, "only GET and PUT are supported")
	}
}

func (s *Server) putSettings(w http.ResponseWriter, r *http.Request) {
	// A JSON content type forces browsers to preflight a cross-site request,
	// which this server never approves.
	if ct := r.Header.Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		writeError(w, http.StatusUnsupportedMediaType, "Content-Type must be application/json")
		return
	}
	var body struct {
		Values map[string]any `json:"values"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&body); err != nil || len(body.Values) == 0 {
		writeError(w, http.StatusBadRequest, `body must be {"values":{...}} with at least one value`)
		return
	}

	cfg := config.Load()
	if cfg == nil {
		cfg = &config.Config{}
	}
	errs := map[string]string{}
	var saved []string
	for key, raw := range body.Values {
		if err := applySetting(cfg, key, raw); err != nil {
			errs[key] = err.Error()
			continue
		}
		saved = append(saved, key)
	}
	if len(errs) > 0 {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnprocessableEntity)
		_ = json.NewEncoder(w).Encode(map[string]any{"error": "invalid settings; nothing was saved", "errors": errs})
		return
	}
	if err := config.Save(cfg); err != nil {
		writeError(w, http.StatusInternalServerError, "saving: "+err.Error())
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"saved": saved, "restart_required": true})
}

func asString(raw any) (string, error) {
	s, ok := raw.(string)
	if !ok {
		return "", fmt.Errorf("must be a string")
	}
	return strings.TrimSpace(s), nil
}

func oneOf(v string, allowed ...string) error {
	for _, a := range allowed {
		if v == a {
			return nil
		}
	}
	return fmt.Errorf("must be one of: %s", strings.Join(allowed, ", "))
}

// existingPath accepts "" (unset) or a path that exists.
func existingPath(v string) (string, error) {
	if v == "" {
		return "", nil
	}
	p := config.ExpandPath(v)
	if _, err := os.Stat(p); err != nil {
		return "", fmt.Errorf("%s does not exist", p)
	}
	return p, nil
}

func applySetting(cfg *config.Config, key string, raw any) error {
	if key == "speaker_threshold" {
		f, ok := raw.(float64)
		if !ok || f < 0 || f > 0.95 {
			return fmt.Errorf("must be a number between 0 and 0.95")
		}
		cfg.SpeakerThreshold = f
		return nil
	}
	v, err := asString(raw)
	if err != nil {
		return err
	}
	switch key {
	case "language":
		if v != "" && !langRe.MatchString(v) {
			return fmt.Errorf("must be empty (auto) or a language code like tr, en")
		}
		cfg.Language = v
	case "backend":
		if err := oneOf(v, "whisper", "onnx", "nemotron", "tc"); err != nil {
			return err
		}
		cfg.Backend = v
	case "execution_provider":
		if err := oneOf(v, "auto", "cuda", "cpu"); err != nil {
			return err
		}
		cfg.ExecutionProvider = v
	case "audio_source":
		if err := oneOf(v, "mic", "speaker", "both"); err != nil {
			return err
		}
		cfg.AudioSource = v
	case "default_model":
		p, err := existingPath(v)
		if err != nil {
			return err
		}
		if p == "" {
			return fmt.Errorf("a model is required")
		}
		cfg.DefaultModel = p
	case "refine_model":
		p, err := existingPath(v)
		if err != nil {
			return err
		}
		cfg.RefineModel = p
	case "hotkey":
		if v != "" && !hotkeyRe.MatchString(strings.ToLower(v)) {
			return fmt.Errorf("use keys joined by +, e.g. ctrl+shift+space")
		}
		cfg.Hotkey = strings.ToLower(v)
	case "output_dir":
		cfg.OutputDir = v
	default:
		return fmt.Errorf("unknown setting")
	}
	return nil
}
