package metrics

import (
	"encoding/json"
	"testing"
	"time"
)

func TestCollector_IncDecEndpoint(t *testing.T) {
	c := New()
	c.IncEndpoint("GET /api/servers")
	c.IncEndpoint("GET /api/servers")
	c.DecEndpoint("GET /api/servers")

	inf := c.EndpointInFlight()
	if got := inf["GET /api/servers"]; got != 1 {
		t.Errorf("expected in-flight = 1, got %d", got)
	}
}

func TestCollector_EndpointSnapshot(t *testing.T) {
	c := New()
	c.IncEndpoint("GET /api/servers/1")
	c.IncEndpoint("POST /api/downloads")
	c.IncEndpoint("GET /api/servers/1")
	c.DecEndpoint("GET /api/servers/1")

	inf := c.EndpointInFlight()
	if len(inf) != 2 {
		t.Errorf("expected 2 route entries, got %d", len(inf))
	}
	if inf["GET /api/servers/1"] != 1 {
		t.Errorf("expected GET /api/servers/1 in-flight = 1, got %d", inf["GET /api/servers/1"])
	}
	if inf["POST /api/downloads"] != 1 {
		t.Errorf("expected POST /api/downloads in-flight = 1, got %d", inf["POST /api/downloads"])
	}
}

func TestCollector_ProviderMetrics(t *testing.T) {
	c := New()
	c.RecordProviderRequest("nibl")
	c.RecordProviderRequest("nibl")
	c.RecordProviderTimeout("nibl")
	c.RecordProviderRequest("subsplease")
	c.RecordProviderFailure("subsplease")

	all := c.AllProviderMetrics()

	// nibl: 2 requests, 1 timeout, 0 failures
	if nibl := all["nibl"]; nibl.Requests != 2 || nibl.Timeouts != 1 || nibl.Failures != 0 {
		t.Errorf("unexpected nibl metrics: %+v", nibl)
	}

	// subsplease: 1 request, 0 timeouts, 1 failure
	if sub := all["subsplease"]; sub.Requests != 1 || sub.Timeouts != 0 || sub.Failures != 1 {
		t.Errorf("unexpected subsplease metrics: %+v", sub)
	}
}

func TestCollector_ShutdownTimings(t *testing.T) {
	c := New()
	c.RecordShutdownTiming("ircmgr", 150*time.Millisecond)
	c.RecordShutdownTiming("queue", 50*time.Millisecond)

	timings := c.ShutdownTimings()
	if timings["ircmgr"] != "150ms" {
		t.Errorf("expected ircmgr = 150ms, got %s", timings["ircmgr"])
	}
	if timings["queue"] != "50ms" {
		t.Errorf("expected queue = 50ms, got %s", timings["queue"])
	}
}

func TestCollector_StatsQueueDepth(t *testing.T) {
	c := New()
	// Before setting function
	if d := c.StatsQueueDepth(); d != -1 {
		t.Errorf("expected -1, got %d", d)
	}

	c.SetStatsQueueDepthFn(func() int { return 42 })
	if d := c.StatsQueueDepth(); d != 42 {
		t.Errorf("expected 42, got %d", d)
	}
}

func TestCollector_SnapshotJSON(t *testing.T) {
	c := New()
	c.IncEndpoint("GET /api/health")
	c.RecordProviderRequest("nibl")
	c.RecordProviderTimeout("xdcc_eu")
	c.RecordShutdownTiming("store", 100*time.Millisecond)
	c.SetStatsQueueDepthFn(func() int { return 7 })

	snap := c.Snapshot()
	_, err := json.Marshal(snap)
	if err != nil {
		t.Fatalf("Snapshot() must produce valid JSON: %v", err)
	}

	// Check expected keys
	keys := []string{"timestamp", "uptime_seconds", "num_goroutines", "endpoints", "providers", "shutdown_timings", "stats_queue_depth"}
	for _, k := range keys {
		if _, ok := snap[k]; !ok {
			t.Errorf("Snapshot missing key %q", k)
		}
	}

	// Verify endpoint data
	endpoints := snap["endpoints"].(map[string]int64)
	if endpoints["GET /api/health"] != 1 {
		t.Errorf("expected GET /api/health = 1, got %d", endpoints["GET /api/health"])
	}

	// Verify provider data
	providers := snap["providers"].(map[string]ProviderMetrics)
	if nibl := providers["nibl"]; nibl.Requests != 1 {
		t.Errorf("expected nibl.Requests = 1, got %d", nibl.Requests)
	}
	if eu := providers["xdcc_eu"]; eu.Timeouts != 1 {
		t.Errorf("expected xdcc_eu.Timeouts = 1, got %d", eu.Timeouts)
	}

	// Verify stats queue depth
	if d := snap["stats_queue_depth"].(int); d != 7 {
		t.Errorf("expected stats_queue_depth = 7, got %d", d)
	}
}
