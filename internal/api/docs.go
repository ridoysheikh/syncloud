package api

import (
	"net/http"
	"sync"
)

// CLICommands maps API operations to synctl commands (set by the
// controller, which links the CLI; the api package cannot import it).
var CLICommands func() map[string]string

var (
	cliOnce sync.Once
	cliOps  map[string]string
)

// handleListCommands maps API operations to synctl commands, so the API
// docs can offer "copy as synctl" next to "copy as curl" (§7.1).
func (s *Server) handleListCommands(w http.ResponseWriter, r *http.Request) {
	cliOnce.Do(func() {
		cliOps = map[string]string{}
		if CLICommands != nil {
			cliOps = CLICommands()
		}
	})
	writeJSON(w, http.StatusOK, map[string]any{"commands": cliOps})
}
