package searchagg

import (
	"context"
	"time"

	"xdcc-go/internal/store"
)

// ---------------------------------------------------------------------------
// Provider insights
// ---------------------------------------------------------------------------

// GetProviderStats returns stats for a specific provider since the given time.
func (a *Aggregator) GetProviderStats(ctx context.Context, provider string, since time.Time) ([]store.ProviderStats, error) {
	return a.store.GetProviderStats(ctx, provider, since)
}

// GetAllProviderStats returns stats for all providers since the given time.
func (a *Aggregator) GetAllProviderStats(ctx context.Context, since time.Time) (map[string][]store.ProviderStats, error) {
	return a.store.GetAllProviderStats(ctx, since)
}

// GetProviderInsights returns a summary of provider health for the dashboard.
type ProviderInsight struct {
	Name         string  `json:"name"`
	Enabled      bool    `json:"enabled"`
	Status       string  `json:"status"` // ok | degraded | down
	Requests     int     `json:"requests_24h"`
	Successes    int     `json:"successes_24h"`
	Failures     int     `json:"failures_24h"`
	Timeouts     int     `json:"timeouts_24h"`
	AvgLatencyMs float64 `json:"avg_latency_ms_24h"`
	SuccessRate  float64 `json:"success_rate_24h"`
}

// GetProviderInsights returns a summary of all provider health.
// Returns partial results if stats query fails — the `enabled` field is
// always correct because it's derived from config/runtime state, not stats.
func (a *Aggregator) GetProviderInsights(ctx context.Context) ([]ProviderInsight, error) {
	since := time.Now().Add(-24 * time.Hour)
	allStats, err := a.store.GetAllProviderStats(ctx, since)
	if err != nil {
		// Non-fatal: return insights without stats data
		a.log.Warnf("GetProviderInsights: GetAllProviderStats failed: %v", err)
	}

	engines := []string{"nibl", "xdcc-eu", "subsplease"}
	insights := make([]ProviderInsight, 0, len(engines))

	for _, name := range engines {
		insight := ProviderInsight{
			Name:    name,
			Enabled: a.IsProviderEnabled(name),
		}

		if allStats != nil {
			stats := allStats[name]
			for _, s := range stats {
				insight.Requests += s.Requests
				insight.Successes += s.Successes
				insight.Failures += s.Failures
				insight.Timeouts += s.Timeouts
				// Weighted average latency
				if s.Requests > 0 {
					insight.AvgLatencyMs = (insight.AvgLatencyMs*float64(insight.Requests-s.Requests) + s.AvgLatencyMs*float64(s.Requests)) / float64(insight.Requests)
				}
			}

			if insight.Requests > 0 {
				insight.SuccessRate = float64(insight.Successes) / float64(insight.Requests) * 100
			}
		}

		// Determine status
		if !insight.Enabled {
			insight.Status = "disabled"
		} else if insight.Requests == 0 {
			insight.Status = "ok"
		} else if insight.SuccessRate >= 80 {
			insight.Status = "ok"
		} else if insight.SuccessRate >= 50 {
			insight.Status = "degraded"
		} else {
			insight.Status = "down"
		}

		insights = append(insights, insight)
	}

	return insights, nil
}
