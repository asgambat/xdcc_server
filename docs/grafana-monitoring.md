# Grafana Monitoring for xdcc_server

How to integrate **Grafana** with the xdcc-server for real-time dashboards, alerts,
and continuous profiling — leveraging the existing metrics and pprof infrastructure.

## What Already Exists

The server already exposes all the data needed for monitoring. No changes to the
application code are *required* — you can start with what's already there.

| Endpoint | Protection | What it provides |
|---|---|---|
| `GET /api/metrics` | `X-Admin-Token` | JSON snapshot of all runtime metrics (goroutines, memory, endpoint in-flight, provider stats, shutdown timings, queue depth) |
| `GET /debug/pprof/` | `X-Admin-Token` | Standard Go pprof index page |
| `GET /debug/pprof/profile?seconds=N` | `X-Admin-Token` | CPU profile (pprof format) |
| `GET /debug/pprof/heap` | `X-Admin-Token` | Heap profile — memory in use |
| `GET /debug/pprof/allocs` | `X-Admin-Token` | Total allocations since start (leak detection) |
| `GET /debug/pprof/goroutine` | `X-Admin-Token` | Goroutine profile |
| `GET /debug/pprof/block` | `X-Admin-Token` | Goroutine blocking profile (requires `--pprof`) |
| `GET /debug/pprof/mutex` | `X-Admin-Token` | Mutex contention profile (requires `--pprof`) |
| `GET /debug/pprof/trace?seconds=N` | `X-Admin-Token` | Execution trace |
| `GET /debug/goroutines` | `X-Admin-Token` | JSON with goroutine count, memory, SSE client count |
| `GET /debug/goroutines/dump` | `X-Admin-Token` | Full text dump of all goroutine stacks |

Enable the `--pprof` CLI flag (or set `profiling.block_profile_rate` and
`profiling.mutex_profile_fraction` in `config.yaml`) to get block and mutex profiles
at maximum detail. Note this adds measurable overhead — use selectively.

See [`docs/pprof-guide.md`](pprof-guide.md) for a complete guide to local profiling
with `go tool pprof`.

### Metrics Included in `/api/metrics`

```
{
  "timestamp":          "2026-05-31T20:30:00Z",
  "uptime_seconds":     3600,
  "num_goroutines":     42,
  "memory_alloc_mb":    48,
  "memory_sys_mb":      72,
  "memory_num_gc":      12,
  "endpoints":          { "GET /api/servers": 2, ... },
  "providers":          { "nibl": { "requests": 45, "timeouts": 2, "failures": 1 }, ... },
  "shutdown_timings":   { "irc": "200ms", "queue": "150ms" },
  "stats_queue_depth":  0
}
```

---

## Strategy 1: Grafana Pyroscope — Continuous Profiling (Zero Code Changes)

[Grafana Pyroscope](https://grafana.com/oss/pyroscope/) is Grafana's continuous
profiling product. It can **scrape pprof endpoints directly** — no code changes needed.

### Architecture

```
xdcc-server  ──pprof──▶  Pyroscope Agent / Alloy  ──▶  Pyroscope Server  ──▶  Grafana
 (port 8080)              (periodic scrape)             (storage + query)      (flame graphs)
```

### Setup with Grafana Alloy (recommended)

Install [Grafana Alloy](https://grafana.com/docs/alloy/latest/) on the same host
and configure it to scrape pprof:

```hcl
// /etc/alloy/config.alloy — scrape xdcc-server pprof
pyroscope.scrape "xdcc" {
  targets = [{
    __address__ = "localhost:8080"
    __default_headers__ = {
      "X-Admin-Token" = "<YOUR_ADMIN_TOKEN>",
    }
  }]

  forward_to = [pyroscope.write.prod.receiver]

  profiling_config {
    profile.fetcher "pprof" {
      enabled = true
      path    = "/debug/pprof"
      delta   = true

      profile.types = {
        cpu = {
          enabled  = true
          path     = "/debug/pprof/profile"
          duration = "15s"
        }
        heap = {
          enabled = true
          path    = "/debug/pprof/heap"
        }
        goroutine = {
          enabled = true
          path    = "/debug/pprof/goroutine"
        }
        allocs = {
          enabled = true
          path    = "/debug/pprof/allocs"
        }
        mutex = {
          enabled = true
          path    = "/debug/pprof/mutex"
        }
        block = {
          enabled = true
          path    = "/debug/pprof/block"
        }
      }
    }
  }
}

pyroscope.write "prod" {
  endpoint {
    url = "http://pyroscope-server:4040"
  }
}
```

### What you get

- **Flame graphs** continuously updated every scrape interval
- **Diff views** — compare CPU/heap between time periods
- **Heatmaps** — spot patterns over hours/days
- **Labels** — filter by service, instance, or custom tags
- **Alerting** — trigger when goroutine count or heap size crosses thresholds

### Docker Compose example

```yaml
services:
  pyroscope:
    image: grafana/pyroscope:latest
    ports: ["4040:4040"]

  alloy:
    image: grafana/alloy:latest
    volumes:
      - ./alloy-config.alloy:/etc/alloy/config.alloy
```

> **Note**: The `X-Admin-Token` header is passed in each scrape. Make sure to
> use the token shown in the server logs on startup. Without a valid token, all
> pprof endpoints return 401.

---

## Strategy 2: Prometheus + Grafana — Metrics Dashboards (~50 lines of code)

This is the gold standard for Go service monitoring. Requires adding
`prometheus/client_golang` as a dependency and registering a `/metrics` endpoint.

### What to add

```go
// In internal/api/handlers_metrics.go or a new file

import (
    "github.com/prometheus/client_golang/prometheus"
    "github.com/prometheus/client_golang/prometheus/promhttp"
)

var (
    goroutinesGauge = prometheus.NewGauge(prometheus.GaugeOpts{
        Name: "xdcc_goroutines",
        Help: "Current number of goroutines.",
    })
    memoryAllocGauge = prometheus.NewGauge(prometheus.GaugeOpts{
        Name: "xdcc_memory_alloc_bytes",
        Help: "Currently allocated heap memory in bytes.",
    })
    endpointInFlightGauge = prometheus.NewGaugeVec(prometheus.GaugeOpts{
        Name: "xdcc_endpoint_requests_in_flight",
        Help: "Currently in-flight HTTP requests per route.",
    }, []string{"route"})
    providerRequestsCounter = prometheus.NewCounterVec(prometheus.CounterOpts{
        Name: "xdcc_provider_requests_total",
        Help: "Total search provider requests.",
    }, []string{"provider"})
    providerTimeoutsCounter = prometheus.NewCounterVec(prometheus.CounterOpts{
        Name: "xdcc_provider_timeouts_total",
        Help: "Total search provider timeouts.",
    }, []string{"provider"})
    providerFailuresCounter = prometheus.NewCounterVec(prometheus.CounterOpts{
        Name: "xdcc_provider_failures_total",
        Help: "Total search provider non-timeout failures.",
    }, []string{"provider"})
)

func init() {
    prometheus.MustRegister(
        goroutinesGauge,
        memoryAllocGauge,
        endpointInFlightGauge,
        providerRequestsCounter,
        providerTimeoutsCounter,
        providerFailuresCounter,
    )
}

// In the router, add (outside the admin-token group — public, or inside if preferred):
//   r.Get("/metrics", promhttp.Handler().ServeHTTP)
```

> **Note:** Unlike the `/api/metrics` JSON endpoint, the Prometheus `/metrics`
> endpoint is **not** protected by admin token by default. You can either:
> - Put it behind `RequireAdminToken` too (then configure `basic_auth` in Prometheus)
> - Keep it public but firewall it (e.g., only allow Prometheus IP)
> - Run Prometheus on localhost alongside the server

### Prometheus scrape config

```yaml
# prometheus.yml
scrape_configs:
  - job_name: xdcc-server
    scrape_interval: 15s
    static_configs:
      - targets: ['localhost:8080']
    # If protected by admin token:
    basic_auth:
      username: admin
      password: '<YOUR_ADMIN_TOKEN>'
```

### What you get

- **Pre-built Go dashboards** — import Grafana dashboard ID `10826` (Go Processes)
  or `6671` (Go Runtime) for instant visibility
- **Custom dashboards** with your `xdcc_*` metrics
- **Alertmanager integration** — e.g. alert when goroutines > 200 or memory > 500 MB
- **Recording rules** — precompute rates/aggregations for faster dashboards

### Custom Dashboard panels to create

| Panel | PromQL query | Why |
|---|---|---|
| **Goroutines** | `xdcc_goroutines` | Baseline: when it creeps up, you have a leak |
| **Memory In-Use** | `xdcc_memory_alloc_bytes / 1024 / 1024` | Track MB over time |
| **Endpoint in-flight** | `xdcc_endpoint_requests_in_flight` | Spot slow or stuck endpoints |
| **Provider request rate** | `rate(xdcc_provider_requests_total[5m])` | Search engine throughput |
| **Provider timeout rate** | `rate(xdcc_provider_timeouts_total[5m])` | Is a provider degrading? |
| **Provider failure rate** | `rate(xdcc_provider_failures_total[5m])` | Provider health over time |

---

## Strategy 3: Grafana JSON API Datasource (Zero Code Changes)

Use the [JSON API plugin](https://grafana.com/grafana/plugins/marcusolsson-json-datasource/)
to query `GET /api/metrics` directly.

### How to set up

1. Install the plugin in Grafana:
   ```bash
   grafana-cli plugins install marcusolsson-json-datasource
   ```

2. Add a new datasource pointing to `http://xdcc-server:8080`

3. Configure the `X-Admin-Token` header in the datasource HTTP settings

4. Create a dashboard with a "JSON API" panel. Example query to extract goroutine count:

   ```
   Fields: $.num_goroutines
   ```

5. Path: `/api/metrics`

### Limitations

| Limitation | Impact |
|---|---|
| **No automatic series** — each field is a separate panel query | Manual setup per metric |
| **Poll-based** — Grafana polls `/api/metrics` every N seconds | Less efficient than Prometheus push/scrape |
| **No rate/irate** — raw values only, no PromQL computations | Can't compute requests/sec or deltas easily |
| **No alerting out of the box** | Need Grafana alerting on JSON fields (limited) |
| **No historical aggregation** | Each poll is a point; no downsampling or retention policies |

### When to use this

Good for: quick ad-hoc checks, one-off dashboards, or when you don't want to add dependencies.

Not good for: production monitoring with alerting, long-term trend analysis, or
multi-instance aggregation.

---

## Comparison

| | Pyroscope | Prometheus | JSON API |
|---|---|---|---|
| **Code changes** | None | ~50 lines | None |
| **Infra to add** | Pyroscope + Alloy | Prometheus | None (Grafana only) |
| **Profiling (flame graphs)** | ✅ best-in-class | ❌ | ❌ |
| **Metrics dashboards** | ❌ (profiling only) | ✅ best-in-class | ⚠️ basic |
| **Alerting** | ✅ (on profile anomalies) | ✅ (Prometheus + Alertmanager) | ⚠️ limited |
| **Go stdlib dashboards** | ❌ | ✅ (IDs 10826, 6671) | ❌ |
| **Rate/delta queries** | N/A | ✅ (PromQL) | ❌ |
| **Long-term storage** | ✅ | ✅ | ❌ |

---

## Recommended Approach

Use **Pyroscope for profiling + Prometheus for metrics**:

1. **Start with Pyroscope** (zero code changes, immediate value) — get flame graphs
   and goroutine visibility without touching the codebase.

2. **Add Prometheus metrics** later when you need:
   - Alerting (e.g. "goroutine count > 200 for 5 minutes")
   - Throughput tracking (provider requests/sec, download completions/sec)
   - Multi-instance aggregation
   - Pre-built Go dashboards

Both run side-by-side; they scrape different endpoints and complement each other.

---

## Access Control Notes

All pprof and metrics endpoints are protected by `X-Admin-Token`. The admin token
is either:
- Configured in `config.yaml` (`security.admin_token`)
- Set via `XDCC_SECURITY_ADMIN_TOKEN` environment variable
- Auto-generated on startup (printed in the logs)

When configuring any scraper (Alloy, Prometheus, Grafana JSON datasource), use the
same token.

Example for `docker-compose.yml`:
```yaml
services:
  xdcc-server:
    environment:
      - XDCC_SECURITY_ADMIN_TOKEN=my-static-token
```

This makes it predictable for monitoring tool configuration.

---

## See Also

- [`docs/pprof-guide.md`](pprof-guide.md) — How to profile locally with `go tool pprof`
- [Grafana Pyroscope docs](https://grafana.com/docs/pyroscope/latest/)
- [Prometheus Go client library](https://github.com/prometheus/client_golang)
- [Grafana JSON API plugin](https://grafana.com/grafana/plugins/marcusolsson-json-datasource/)
- [Grafana Alloy docs](https://grafana.com/docs/alloy/latest/)
