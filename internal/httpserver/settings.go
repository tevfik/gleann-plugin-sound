package httpserver

import (
	"net/http"

	"github.com/tevfik/gleann-plugin-sound/internal/config"
)

// handleSettings serves the generic settings form (GET/PUT /settings, see
// github.com/tevfik/gleann/modules/settings) for config.Settings, so a client
// such as the gleann web UI edits the same fields as the TUI.
func (s *Server) handleSettings(w http.ResponseWriter, r *http.Request) {
	config.Settings().Handler(s.authorized).ServeHTTP(w, r)
}
