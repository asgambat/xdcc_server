package api

import (
	"encoding/json"
	"net/http"
	"runtime"
	"runtime/pprof"
	"time"
)

// =========================================================================
// Debug endpoints for goroutine profiling and diagnostics
// =========================================================================

// handleDebugGoroutines returns detailed goroutine information.
func (a *API) handleDebugGoroutines(w http.ResponseWriter, r *http.Request) {
	// Get all goroutine profiles
	profiles := pprof.Profiles()
	var goroutineProfile *pprof.Profile
	for _, p := range profiles {
		if p.Name() == "goroutine" {
			goroutineProfile = p
			break
		}
	}

	numGoroutines := runtime.NumGoroutine()

	response := map[string]interface{}{
		"timestamp":         time.Now().Format(time.RFC3339),
		"num_goroutines":    numGoroutines,
		"sse_clients":       a.SSEHub.ClientCount(),
		"profile_available": goroutineProfile != nil,
	}

	// Add memory stats
	var m runtime.MemStats
	runtime.ReadMemStats(&m)
	response["memory"] = map[string]interface{}{
		"alloc_mb":       m.Alloc / 1024 / 1024,
		"total_alloc_mb": m.TotalAlloc / 1024 / 1024,
		"sys_mb":         m.Sys / 1024 / 1024,
		"num_gc":         m.NumGC,
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(response)
}

// handleDebugGoroutinesDump returns a text dump of all goroutine stacks.
func (a *API) handleDebugGoroutinesDump(w http.ResponseWriter, r *http.Request) {
	profile := pprof.Lookup("goroutine")
	if profile == nil {
		http.Error(w, "goroutine profile not found", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/plain")
	w.Header().Set("Content-Disposition", "attachment; filename=goroutines.txt")

	// debug=2 gives full stack traces
	_ = profile.WriteTo(w, 2)
}
