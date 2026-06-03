package api

import (
	"net/http"
)

// =========================================================================
// GET /api/metrics — runtime metrics snapshot
// =========================================================================

func (a *API) handleMetrics(w http.ResponseWriter, r *http.Request) {
	if a.Metrics == nil {
		writeError(w, http.StatusServiceUnavailable, "METRICS_UNAVAILABLE", "Metrics collector not available")
		return
	}

	writeJSON(w, http.StatusOK, a.Metrics.Snapshot())
}
