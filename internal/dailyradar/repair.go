package dailyradar

import "net/http"

// RepairSourceRepositories removes generator/source repositories that were
// incorrectly treated as article-related projects. The operation is fixed to
// the known Daily Radar sources; callers cannot use it to hide arbitrary repos.
func (h *Handler) RepairSourceRepositories(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	repairer, ok := h.store.(interface {
		RepairRadarSourceRepositories() (int, error)
	})
	if !ok {
		writeError(w, http.StatusNotImplemented, "REPAIR_UNSUPPORTED", "store does not support repair")
		return
	}
	changed, err := repairer.RepairRadarSourceRepositories()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "repair failed")
		return
	}
	writeSuccess(w, http.StatusOK, map[string]int{"changed": changed})
}
