package config

import (
	"context"
	"fmt"
	"strings"

	"github.com/tevfik/gleann/modules/settings"
)

// onnxRuntimeID is the catalog id of the ONNX Runtime library.
const onnxRuntimeID = "onnxruntime"

// modelGroup names the catalog section a model belongs to.
func modelGroup(m WhisperModel) string {
	switch {
	case m.Name == "silero-vad":
		return "Voice activity detection"
	case IsRefineModel(m.Name):
		return "Refiner (checks each utterance)"
	case len(m.Name) > 8 && m.Name[:8] == "nemotron":
		return "Nemotron (live)"
	case len(m.Name) > 5 && m.Name[:5] == "onnx-":
		return "Whisper (ONNX Runtime)"
	}
	return "Whisper (whisper.cpp)"
}

// Catalog offers the models of AllAvailableModels and the ONNX Runtime for
// download (GET/POST /models), the same list `models download` and the
// Setup screen use. A downloaded transcription model is added to the
// config's model list, so the Model setting offers it at once.
func Catalog() *settings.Catalog {
	return &settings.Catalog{
		List: func() []settings.Model {
			var out []settings.Model
			// Transcription models first, helpers last.
			var models []WhisperModel
			models = append(models, AvailableModels()...)
			models = append(models, AvailableONNXModels()...)
			models = append(models, AvailableNemotronModels()...)
			models = append(models, AvailableRefineModels()...)
			models = append(models, SileroVADModel())
			for _, m := range models {
				size := m.Size
				if strings.Contains(m.DisplayName, size) {
					size = "" // the display name says it already
				}
				out = append(out, settings.Model{
					ID: m.Name, Name: m.DisplayName, Group: modelGroup(m), Size: size,
					Value: ModelPath(m.FileName), Installed: IsModelDownloaded(m.FileName),
				})
			}
			if info := GetONNXRuntimeInfo(); info != nil {
				out = append(out, settings.Model{
					ID: onnxRuntimeID, Name: "ONNX Runtime " + ONNXRuntimeVersion, Group: "Runtime",
					Note:      "needed by the onnx and nemotron backends",
					Installed: IsONNXRuntimeDownloaded(),
				})
			}
			return out
		},
		Download: func(_ context.Context, id string, report func(settings.Progress)) error {
			if id == onnxRuntimeID {
				report(settings.Progress{Message: "downloading ONNX Runtime"})
				_, err := DownloadONNXRuntime()
				return err
			}
			m := FindModel(id)
			if m == nil {
				return fmt.Errorf("unknown model %q", id)
			}
			err := DownloadModelWithProgress(*m, func(p DownloadProgress) {
				msg := p.Message
				if msg == "" && p.TotalFiles > 1 {
					msg = fmt.Sprintf("file %d of %d: %s", p.FileIndex, p.TotalFiles, p.FileName)
				}
				report(settings.Progress{Done: p.BytesRead, Total: p.TotalBytes, Message: msg})
			})
			if err != nil {
				return err
			}
			return RegisterModel(*m)
		},
	}
}

// RegisterModel adds a downloaded transcription or refine model to the
// config's model list (the choices of the Model settings). VAD models are
// not listed there.
func RegisterModel(m WhisperModel) error {
	if m.Name == "silero-vad" {
		return nil
	}
	cfg := Load()
	if cfg == nil {
		cfg = &Config{}
	}
	path := ModelPath(m.FileName)
	for _, e := range cfg.Models {
		if e.Name == m.Name || e.Path == path {
			return nil
		}
	}
	lang := "en"
	if m.Multilingual {
		lang = "multilingual"
	}
	cfg.Models = append(cfg.Models, ModelEntry{Name: m.Name, Path: path, Size: m.Size, Language: lang})
	return Save(cfg)
}
