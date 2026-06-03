// Package metrics provides a central collector for runtime metrics across all
// application components: HTTP endpoint concurrency, provider timeouts, shutdown
// durations, and async queue depths.
package metrics

import (
	"runtime"
	"sync"
	"sync/atomic"
	"time"
)

// ---------------------------------------------------------------------------
// Collector
// ---------------------------------------------------------------------------

// Collector holds all runtime metrics for the application. All exported methods
// are safe for concurrent use.
type Collector struct {
	mu sync.RWMutex

	// endpointInFlight tracks the number of currently processing HTTP requests
	// per route pattern, using a lock-free sync.Map to avoid mutex contention
	// under high-throughput HTTP traffic. The key is the chi route pattern
	// (e.g. "GET /api/servers/{serverID}").
	// This is effectively the goroutine count serving each endpoint.
	endpointInFlight sync.Map // map[string]*atomic.Int64

	// providerTimeouts counts total timeout occurrences per provider name.
	providerTimeouts sync.Map // map[string]*atomic.Int64

	// providerRequests counts total request count per provider name.
	providerRequests sync.Map // map[string]*atomic.Int64

	// providerFailures counts total failures per provider name (non-timeout errors).
	providerFailures sync.Map // map[string]*atomic.Int64

	// shutdownTimings records the duration of each shutdown step per component.
	shutdownTimings map[string]time.Duration

	// statsQueueDepthFn, if set, returns the current number of buffered provider
	// stats records waiting to be written to the database.
	statsQueueDepthFn func() int

	// startedAt records when the collector was created.
	startedAt time.Time
}

// New creates a new metrics collector.
func New() *Collector {
	return &Collector{
		shutdownTimings: make(map[string]time.Duration),
		startedAt:       time.Now(),
	}
}

// ---------------------------------------------------------------------------
// Endpoint in-flight tracking
// ---------------------------------------------------------------------------

// IncEndpoint increments the in-flight counter for the given route pattern.
// It lazily creates the counter on first access.
func (c *Collector) IncEndpoint(route string) {
	c.loadOrInitInt64(&c.endpointInFlight, route).Add(1)
}

// DecEndpoint decrements the in-flight counter for the given route pattern.
func (c *Collector) DecEndpoint(route string) {
	v, ok := c.endpointInFlight.Load(route)
	if ok {
		if cnt, ok := v.(*atomic.Int64); ok {
			cnt.Add(-1)
		}
	}
}

// EndpointInFlight returns a snapshot of in-flight requests per route pattern.
func (c *Collector) EndpointInFlight() map[string]int64 {
	out := make(map[string]int64)
	c.endpointInFlight.Range(func(key, val interface{}) bool {
		route, _ := key.(string)
		if cnt, ok := val.(*atomic.Int64); ok {
			out[route] = cnt.Load()
		}
		return true
	})
	return out
}

// ---------------------------------------------------------------------------
// Provider metrics
// ---------------------------------------------------------------------------

// RecordProviderTimeout increments the timeout counter for the given provider.
func (c *Collector) RecordProviderTimeout(name string) {
	c.loadOrInitInt64(&c.providerTimeouts, name).Add(1)
}

// RecordProviderRequest increments the request counter for the given provider.
func (c *Collector) RecordProviderRequest(name string) {
	c.loadOrInitInt64(&c.providerRequests, name).Add(1)
}

// RecordProviderFailure increments the failure counter for the given provider.
func (c *Collector) RecordProviderFailure(name string) {
	c.loadOrInitInt64(&c.providerFailures, name).Add(1)
}

// ProviderMetrics returns a snapshot of all provider metrics.
type ProviderMetrics struct {
	Requests int64 `json:"requests"`
	Timeouts int64 `json:"timeouts"`
	Failures int64 `json:"failures"` // non-timeout failures only (see aggregator.go)
}

// AllProviderMetrics returns all provider metrics.
func (c *Collector) AllProviderMetrics() map[string]ProviderMetrics {
	out := make(map[string]ProviderMetrics)

	c.providerRequests.Range(func(key, val interface{}) bool {
		name, _ := key.(string)
		pm := out[name]
		if cnt, ok := val.(*atomic.Int64); ok {
			pm.Requests = cnt.Load()
		}
		out[name] = pm
		return true
	})
	c.providerTimeouts.Range(func(key, val interface{}) bool {
		name, _ := key.(string)
		pm := out[name]
		if cnt, ok := val.(*atomic.Int64); ok {
			pm.Timeouts = cnt.Load()
		}
		out[name] = pm
		return true
	})
	c.providerFailures.Range(func(key, val interface{}) bool {
		name, _ := key.(string)
		pm := out[name]
		if cnt, ok := val.(*atomic.Int64); ok {
			pm.Failures = cnt.Load()
		}
		out[name] = pm
		return true
	})
	return out
}

// ---------------------------------------------------------------------------
// Shutdown timing
// ---------------------------------------------------------------------------

// RecordShutdownTiming records the duration of a shutdown step.
func (c *Collector) RecordShutdownTiming(component string, dur time.Duration) {
	c.mu.Lock()
	c.shutdownTimings[component] = dur
	c.mu.Unlock()
}

// ShutdownTimings returns a copy of all shutdown step durations.
func (c *Collector) ShutdownTimings() map[string]string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	out := make(map[string]string, len(c.shutdownTimings))
	for comp, dur := range c.shutdownTimings {
		out[comp] = dur.Round(time.Millisecond).String()
	}
	return out
}

// ---------------------------------------------------------------------------
// Queue depth
// ---------------------------------------------------------------------------

// SetStatsQueueDepthFn sets the function that returns the current provider
// stats channel depth.
func (c *Collector) SetStatsQueueDepthFn(fn func() int) {
	c.mu.Lock()
	c.statsQueueDepthFn = fn
	c.mu.Unlock()
}

// StatsQueueDepth returns the current provider stats queue depth, or -1 if
// no function is set.
func (c *Collector) StatsQueueDepth() int {
	c.mu.RLock()
	fn := c.statsQueueDepthFn
	c.mu.RUnlock()
	if fn == nil {
		return -1
	}
	return fn()
}

// ---------------------------------------------------------------------------
// Snapshot
// ---------------------------------------------------------------------------

// Snapshot returns a complete snapshot of all runtime metrics.
func (c *Collector) Snapshot() map[string]interface{} {
	var m runtime.MemStats
	runtime.ReadMemStats(&m)

	out := map[string]interface{}{
		"timestamp":         time.Now().Format(time.RFC3339),
		"uptime_seconds":    int64(time.Since(c.startedAt).Seconds()),
		"num_goroutines":    runtime.NumGoroutine(),
		"memory_alloc_mb":   m.Alloc / 1024 / 1024,
		"memory_sys_mb":     m.Sys / 1024 / 1024,
		"memory_num_gc":     m.NumGC,
		"endpoints":         c.EndpointInFlight(),
		"providers":         c.AllProviderMetrics(),
		"shutdown_timings":  c.ShutdownTimings(),
		"stats_queue_depth": c.StatsQueueDepth(),
	}
	return out
}

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

func (c *Collector) loadOrInitInt64(m interface {
	LoadOrStore(interface{}, interface{}) (interface{}, bool)
}, key string) *atomic.Int64 {
	v, _ := m.LoadOrStore(key, new(atomic.Int64))
	cnt, _ := v.(*atomic.Int64)
	return cnt
}
