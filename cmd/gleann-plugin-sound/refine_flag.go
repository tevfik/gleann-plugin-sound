package main

import (
	"strings"

	"github.com/tevfik/gleann-plugin-sound/internal/config"
)

// resolveRefineModel returns the GGUF refiner model to use: the flag if given,
// otherwise "refine_model" from sound.json. "off" (or "none") disables refinement
// even when the config sets a model.
func resolveRefineModel(flag string) string {
	switch strings.ToLower(strings.TrimSpace(flag)) {
	case "off", "none", "no":
		return ""
	case "":
		if cfg := config.Load(); cfg != nil {
			return cfg.RefineModel
		}
		return ""
	}
	return flag
}
