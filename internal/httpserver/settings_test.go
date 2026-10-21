package httpserver

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tevfik/gleann-plugin-sound/internal/config"
	"github.com/tevfik/gleann-plugin-sound/internal/core"
)

// settingsServer runs the handler with HOME pointing at a scratch directory
// holding a sound.json, so tests never touch the user's real settings.
func settingsServer(t *testing.T, token string) (*httptest.Server, string) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	model := filepath.Join(home, "model.gguf")
	os.WriteFile(model, []byte("x"), 0o644)
	if err := config.Save(&config.Config{
		DefaultModel: model, Language: "tr", Backend: "tc", Hotkey: "ctrl+shift+space",
		Completed: true, InstallPath: "/keep/me",
		Models: []config.ModelEntry{{Name: "m", Path: model, Size: "1 B"}},
	}); err != nil {
		t.Fatal(err)
	}
	s := New(func() (core.Transcriber, error) { return &fakeEngine{}, nil }, "tr", 0)
	if token != "" {
		s.token = token
	}
	ts := httptest.NewServer(s.Handler())
	t.Cleanup(ts.Close)
	return ts, model
}

func put(t *testing.T, url string, values map[string]any, ct string) *http.Response {
	t.Helper()
	b, _ := json.Marshal(map[string]any{"values": values})
	req, _ := http.NewRequest(http.MethodPut, url, bytes.NewReader(b))
	req.Header.Set("Content-Type", ct)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { resp.Body.Close() })
	return resp
}

func TestSettingsGetDescribesFormWithCurrentValues(t *testing.T) {
	ts, model := settingsServer(t, "")
	resp, err := http.Get(ts.URL + "/settings")
	if err != nil || resp.StatusCode != 200 {
		t.Fatalf("GET: %v %v", resp, err)
	}
	var got struct {
		SchemaVersion int `json:"schema_version"`
		Fields        []struct {
			Key, Type string
			Value     any
			Options   []settingOption
		}
	}
	json.NewDecoder(resp.Body).Decode(&got)
	if got.SchemaVersion != SettingsSchemaVersion {
		t.Errorf("schema_version = %d", got.SchemaVersion)
	}
	byKey := map[string]int{}
	for i, f := range got.Fields {
		byKey[f.Key] = i
	}
	for _, k := range []string{"language", "backend", "default_model", "refine_model", "audio_source", "hotkey", "speaker_threshold"} {
		if _, ok := byKey[k]; !ok {
			t.Errorf("missing field %s", k)
		}
	}
	if v := got.Fields[byKey["language"]].Value; v != "tr" {
		t.Errorf("language value = %v", v)
	}
	if v := got.Fields[byKey["default_model"]]; v.Value != model || len(v.Options) != 1 {
		t.Errorf("default_model = %+v", v)
	}
}

func TestSettingsPutSavesAndKeepsOtherConfig(t *testing.T) {
	ts, _ := settingsServer(t, "")
	resp := put(t, ts.URL+"/settings", map[string]any{
		"language": "en", "backend": "whisper", "hotkey": "Ctrl+Alt+K", "audio_source": "mic", "speaker_threshold": 0.5,
	}, "application/json")
	if resp.StatusCode != 200 {
		t.Fatalf("PUT status %d", resp.StatusCode)
	}
	var out struct {
		Saved           []string
		RestartRequired bool `json:"restart_required"`
	}
	json.NewDecoder(resp.Body).Decode(&out)
	if len(out.Saved) != 5 || !out.RestartRequired {
		t.Errorf("response = %+v", out)
	}
	cfg := config.Load()
	if cfg.Language != "en" || cfg.Backend != "whisper" || cfg.Hotkey != "ctrl+alt+k" || cfg.AudioSource != "mic" || cfg.SpeakerThreshold != 0.5 {
		t.Errorf("config not updated: %+v", cfg)
	}
	if cfg.InstallPath != "/keep/me" || !cfg.Completed || len(cfg.Models) != 1 {
		t.Errorf("unrelated config was lost: %+v", cfg)
	}
}

func TestSettingsPutRejectsBadValuesAtomically(t *testing.T) {
	ts, _ := settingsServer(t, "")
	resp := put(t, ts.URL+"/settings", map[string]any{
		"language": "en", // valid, but must not be saved alongside the invalid ones
		"backend":  "magic", "default_model": "/no/such/file", "hotkey": "Ctrl + !", "speaker_threshold": 7, "bogus": "x",
	}, "application/json")
	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("status %d, want 422", resp.StatusCode)
	}
	var out struct{ Errors map[string]string }
	json.NewDecoder(resp.Body).Decode(&out)
	for _, k := range []string{"backend", "default_model", "hotkey", "speaker_threshold", "bogus"} {
		if out.Errors[k] == "" {
			t.Errorf("no error for %s: %v", k, out.Errors)
		}
	}
	if cfg := config.Load(); cfg.Language != "tr" || cfg.Backend != "tc" {
		t.Errorf("a rejected request changed the config: %+v", cfg)
	}
}

func TestSettingsRequiresJSONAndToken(t *testing.T) {
	ts, _ := settingsServer(t, "s3cret")
	if r, _ := http.Get(ts.URL + "/settings"); r.StatusCode != http.StatusUnauthorized {
		t.Errorf("GET without token = %d", r.StatusCode)
	}
	if r, _ := http.Get(ts.URL + "/settings?token=s3cret"); r.StatusCode != 200 {
		t.Errorf("GET with token = %d", r.StatusCode)
	}
	if r := put(t, ts.URL+"/settings?token=s3cret", map[string]any{"language": "en"}, "text/plain"); r.StatusCode != http.StatusUnsupportedMediaType {
		t.Errorf("PUT as text/plain = %d", r.StatusCode)
	}
	if r := put(t, ts.URL+"/settings", map[string]any{"language": "en"}, "application/json"); r.StatusCode != http.StatusUnauthorized {
		t.Errorf("PUT without token = %d", r.StatusCode)
	}
	if cfg := config.Load(); cfg.Language != "tr" {
		t.Error("unauthorised requests must not change anything")
	}
}

func TestInfoAdvertisesSettings(t *testing.T) {
	ts, _ := settingsServer(t, "")
	resp, _ := http.Get(ts.URL + "/info")
	b := new(strings.Builder)
	var m map[string]any
	json.NewDecoder(resp.Body).Decode(&m)
	_ = b
	if s, ok := m["settings"].(map[string]any); !ok || s["path"] != "/settings" {
		t.Errorf("info = %v", m)
	}
}
