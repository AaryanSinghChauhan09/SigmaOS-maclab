package server

import (
	"net/http"

	"github.com/omacom/omarchy-maclab/internal/api"
)

// The serial console is out of Omarchy Mac Lab: a Hard reset is the only
// out-of-band function. Every way the old lab had into the console answers
// with the same plain line and asks no controller: the three console
// routes, which the browser's terminal used too, the serial log, lab exec's
// serial path (see hExec) and the serial-assisted recovery mode.

// errNoConsole is that refusal.
var errNoConsole = refuse(http.StatusGone, "%s", api.NoSerialConsole)

func (s *Server) hNoConsole(w http.ResponseWriter, r *http.Request) {
	httpErr(w, http.StatusGone, "%s", api.NoSerialConsole)
}
