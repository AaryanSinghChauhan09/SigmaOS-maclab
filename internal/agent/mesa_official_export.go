package agent

import (
	"github.com/omacom/omarchy-maclab/internal/api"
	"github.com/omacom/omarchy-maclab/internal/mesa"
)

var officialExportFiles = mesa.OfficialExportNames()

func validateOfficialExport(out, job, boot, kernel, manifest string, result *api.MesaOfficialResult) error {
	return mesa.ValidateOfficialExport(out, job, boot, kernel, manifest, result)
}
