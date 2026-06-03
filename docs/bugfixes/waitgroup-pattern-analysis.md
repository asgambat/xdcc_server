# WaitGroup Lifecycle Pattern - Future Application Analysis

**Date:** 2026-05-22  
**Status:** Analysis Complete  
**Related Fix:** [Race Condition Fix - ircmanager](../../.github/RACE_FIX_SUMMARY.md)

---

## 📋 Executive Summary

This document analyzes where the **WaitGroup lifecycle pattern** (successfully implemented in `internal/ircmanager`) should be applied to other components to prevent race conditions and improve robustness.

**Pattern Benefits:**
- ✅ Thread-safe goroutine lifecycle management
- ✅ Prevents duplicate Start() calls
- ✅ Deterministic shutdown (no arbitrary timeouts)
- ✅ Idiomatic Go code
- ✅ Better observability (IsRunning checks)

---

## 🎯 Priority Matrix

| Component | Priority | Current Issue | Implementation Effort | Impact |
|-----------|----------|---------------|----------------------|--------|
| **QueueManager** | ⭐⭐⭐⭐⭐ HIGH | No duplicate Start() protection | MEDIUM (2-3 hours) | HIGH |
| **Aggregator** | ⭐⭐⭐⭐ MEDIUM-HIGH | cleanupLoop race potential | LOW (1 hour) | MEDIUM |
| **Monitor** | ⭐⭐⭐ MEDIUM | Future-proofing only | LOW (30 min if needed) | LOW |
| **Hub** | ⭐⭐ LOW | No goroutines currently | N/A | N/A |

---

## 1. 🔴 HIGH PRIORITY: `internal/queue/manager.go` - QueueManager

### Current Implementation

```go
type QueueManager struct {
    store store.Store
    cfg   *config.Config
    log   *log.Logger
    
    // Lifecycle
    ctx    context.Context
    cancel context.CancelFunc
    done   chan struct{}  // ← Used for monitorLoop
    
    // Download workers (already uses WaitGroup ✅)
    downloadWg sync.WaitGroup
    
    // Disk monitor (uses "done channel" pattern)
    stopDiskCheck func()
    diskCheckDone <-chan struct{}
    
    // State
    mu              sync.RWMutex
    activeJobs      map[int64]context.CancelFunc
    channelSlots    map[string]int64
    globalCount     int
    startupReady    chan struct{}
}

func (qm *QueueManager) Start() error {
    go qm.monitorLoop()  // ← NO protection from duplicate calls
    // ...
}

func (qm *QueueManager) Stop() {
    qm.cancel()
    <-qm.done  // ← Waits for monitorLoop
    
    // Download workers already use WaitGroup correctly ✅
    qm.downloadWg.Wait()
}

func (qm *QueueManager) monitorLoop() {
    defer close(qm.done)
    ticker := time.NewTicker(10 * time.Second)
    defer ticker.Stop()
    
    for {
        select {
        case <-qm.ctx.Done():
            return
        case <-ticker.C:
            qm.tryDispatch()
        }
    }
}
```

### Problems Identified

1. ❌ **No duplicate Start() protection**
   - Multiple `go qm.monitorLoop()` possible
   - Second call would panic (`close of closed channel`)

2. ❌ **Disk monitor uses "done channel" pattern**
   - `diskCheckDone` could benefit from WaitGroup consistency

3. ⚠️ **Mixed patterns**
   - `downloadWg` uses WaitGroup ✅
   - `monitorLoop` uses done channel ❌
   - Inconsistent approach

### Recommended Implementation

```go
type QueueManager struct {
    store store.Store
    cfg   *config.Config
    log   *log.Logger
    
    // Lifecycle management with WaitGroup pattern
    ctx    context.Context
    cancel context.CancelFunc
    
    // monitorLoop lifecycle
    monitorWg      sync.WaitGroup
    monitorRunning bool
    monitorRunMu   sync.Mutex
    
    // Download workers lifecycle (already correct ✅)
    downloadWg     sync.WaitGroup
    
    // Disk monitor lifecycle (unified approach)
    diskMonWg      sync.WaitGroup
    diskMonCancel  context.CancelFunc
    
    // State (unchanged)
    mu              sync.RWMutex
    activeJobs      map[int64]context.CancelFunc
    channelSlots    map[string]int64
    globalCount     int
    startupReady    chan struct{}
}

// Start begins the queue manager
func (qm *QueueManager) Start() error {
    // Prevent duplicate calls
    qm.monitorRunMu.Lock()
    if qm.monitorRunning {
        qm.monitorRunMu.Unlock()
        return fmt.Errorf("queue manager already started")
    }
    qm.monitorRunning = true
    qm.monitorRunMu.Unlock()
    
    // Start monitor loop
    qm.monitorWg.Add(1)
    go qm.monitorLoop()
    
    // Start disk monitoring if configured
    if qm.diskMon != nil {
        qm.startDiskMonitor()
    }
    
    // Handle startup delay
    if qm.cfg.Download.StartupDelayMinutes > 0 {
        delay := time.Duration(qm.cfg.Download.StartupDelayMinutes) * time.Minute
        qm.log.Printf("queue manager: delaying dispatch by %v", delay)
        time.AfterFunc(delay, func() {
            close(qm.startupReady)
            qm.tryDispatch()
        })
    } else {
        close(qm.startupReady)
        qm.tryDispatch()
    }
    
    return nil
}

// Stop cancels all downloads and stops all goroutines
func (qm *QueueManager) Stop() {
    // Stop disk monitor first
    qm.stopDiskMonitor()
    
    // Cancel context to signal all goroutines
    qm.cancel()
    
    // Wait for monitor loop (deterministic, no timeout needed)
    qm.monitorWg.Wait()
    
    // Save progress and cancel active downloads
    qm.mu.RLock()
    ids := make([]int64, 0, len(qm.activeJobs))
    for id := range qm.activeJobs {
        ids = append(ids, id)
    }
    qm.mu.RUnlock()
    
    for _, id := range ids {
        if d, err := qm.store.GetDownload(id); err == nil && d != nil && d.ProgressBytes > 0 {
            qm.log.Printf("shutdown: saving progress for download %d", id)
        }
        qm.CancelDownload(id, "server shutting down")
    }
    
    // Wait for all download workers (already correct ✅)
    done := make(chan struct{})
    go func() {
        qm.downloadWg.Wait()
        close(done)
    }()
    
    select {
    case <-done:
        qm.log.Printf("all download workers stopped cleanly")
    case <-time.After(10 * time.Second):
        qm.log.Printf("WARNING: download workers did not stop within 10s, continuing...")
        <-done // Wait anyway
    }
}

// monitorLoop periodically dispatches queued downloads
func (qm *QueueManager) monitorLoop() {
    defer qm.monitorWg.Done()
    defer func() {
        qm.monitorRunMu.Lock()
        qm.monitorRunning = false
        qm.monitorRunMu.Unlock()
    }()
    
    ticker := time.NewTicker(10 * time.Second)
    defer ticker.Stop()
    
    for {
        select {
        case <-qm.ctx.Done():
            return
        case <-ticker.C:
            qm.tryDispatch()
        }
    }
}

// IsRunning returns whether the queue manager is actively running
func (qm *QueueManager) IsRunning() bool {
    qm.monitorRunMu.Lock()
    defer qm.monitorRunMu.Unlock()
    return qm.monitorRunning
}

// startDiskMonitor begins disk space monitoring
func (qm *QueueManager) startDiskMonitor() {
    if qm.diskMon == nil {
        return
    }
    
    ctx, cancel := context.WithCancel(qm.ctx)
    qm.diskMonCancel = cancel
    
    qm.diskMonWg.Add(1)
    go func() {
        defer qm.diskMonWg.Done()
        
        ticker := time.NewTicker(30 * time.Second)
        defer ticker.Stop()
        
        for {
            select {
            case <-ctx.Done():
                return
            case <-ticker.C:
                _, _, low, err := qm.diskMon.Check()
                if err != nil {
                    qm.log.Printf("disk check error: %v", err)
                    continue
                }
                
                qm.mu.Lock()
                wasLow := qm.diskLow
                qm.diskLow = low
                qm.mu.Unlock()
                
                if low && !wasLow {
                    qm.log.Printf("DISK LOW: pausing queue")
                    qm.emitEvent(Event{Type: EventDiskSpaceLow})
                } else if !low && wasLow {
                    qm.log.Printf("DISK OK: resuming queue")
                    qm.emitEvent(Event{Type: EventDiskSpaceOK})
                    qm.tryDispatch()
                }
            }
        }
    }()
}

// stopDiskMonitor stops disk space monitoring
func (qm *QueueManager) stopDiskMonitor() {
    if qm.diskMonCancel != nil {
        qm.diskMonCancel()
        qm.diskMonWg.Wait()
    }
}
```

### Benefits

1. ✅ **Prevents duplicate Start()** → Single monitor loop guaranteed
2. ✅ **Unified lifecycle pattern** → All goroutines use WaitGroup
3. ✅ **Deterministic shutdown** → No arbitrary timeouts for monitorLoop
4. ✅ **Better observability** → `IsRunning()` method for health checks
5. ✅ **Future-proof** → Easy to add more background workers

### Testing Strategy

```go
// Test duplicate Start() prevention
func TestQueueManager_DuplicateStart(t *testing.T) {
    qm := NewQueueManager(...)
    
    err1 := qm.Start()
    require.NoError(t, err1)
    
    err2 := qm.Start()
    require.Error(t, err2) // Should fail
    
    qm.Stop()
}

// Test clean shutdown
func TestQueueManager_CleanShutdown(t *testing.T) {
    qm := NewQueueManager(...)
    qm.Start()
    
    time.Sleep(100 * time.Millisecond)
    require.True(t, qm.IsRunning())
    
    start := time.Now()
    qm.Stop()
    elapsed := time.Since(start)
    
    require.False(t, qm.IsRunning())
    require.Less(t, elapsed, 5*time.Second)
}
```

---

## 2. 🟡 MEDIUM-HIGH PRIORITY: `internal/searchagg/aggregator.go` - Aggregator

### Current Implementation

```go
type Aggregator struct {
    store    store.Store
    cfg      *config.SearchConfig
    log      *log.Logger
    cache    *searchCache
    disabled map[string]bool
    mu       sync.RWMutex
    
    // Cleanup goroutine lifecycle
    ctx    context.Context
    cancel context.CancelFunc
    done   chan struct{}  // ← cleanupLoop
}

func (a *Aggregator) Start(ctx context.Context) error {
    a.ctx, a.cancel = context.WithCancel(ctx)
    go a.cleanupLoop()  // ← NO protection from duplicate calls
    return nil
}

func (a *Aggregator) Stop() {
    if a.cancel != nil {  // ← Fragile nil check
        a.cancel()
        <-a.done
    }
}

func (a *Aggregator) cleanupLoop() {
    defer close(a.done)  // ← Panic if called twice
    
    ticker := time.NewTicker(6 * time.Hour)
    defer ticker.Stop()
    
    for {
        select {
        case <-a.ctx.Done():
            return
        case <-ticker.C:
            a.cleanupStaleEntries()
        }
    }
}
```

### Problems Identified

1. ❌ **No duplicate Start() protection**
   - Multiple `go a.cleanupLoop()` possible
   - `a.ctx` overwritten on second Start()

2. ❌ **Fragile nil check in Stop()**
   - `if a.cancel != nil` prone to errors
   - Not idempotent

3. ⚠️ **done channel pattern**
   - Same issue as ircmanager (before fix)

### Recommended Implementation

```go
type Aggregator struct {
    store    store.Store
    cfg      *config.SearchConfig
    log      *log.Logger
    cache    *searchCache
    disabled map[string]bool
    mu       sync.RWMutex
    
    // Lifecycle management with WaitGroup pattern
    ctx          context.Context
    cancel       context.CancelFunc
    wg           sync.WaitGroup
    isRunning    bool
    runningMu    sync.Mutex
    shutdownOnce sync.Once
}

func (a *Aggregator) Start(ctx context.Context) error {
    // Prevent duplicate calls
    a.runningMu.Lock()
    if a.isRunning {
        a.runningMu.Unlock()
        return fmt.Errorf("aggregator already started")
    }
    a.isRunning = true
    a.runningMu.Unlock()
    
    a.ctx, a.cancel = context.WithCancel(ctx)
    a.wg.Add(1)
    go a.cleanupLoop()
    
    return nil
}

func (a *Aggregator) Stop() {
    // Ensure Stop() is idempotent
    a.shutdownOnce.Do(func() {
        if a.cancel != nil {
            a.cancel()
        }
        a.wg.Wait()
    })
}

func (a *Aggregator) cleanupLoop() {
    defer a.wg.Done()
    defer func() {
        a.runningMu.Lock()
        a.isRunning = false
        a.runningMu.Unlock()
    }()
    
    ticker := time.NewTicker(6 * time.Hour)
    defer ticker.Stop()
    
    for {
        select {
        case <-a.ctx.Done():
            return
        case <-ticker.C:
            a.cleanupStaleEntries()
        }
    }
}

// IsRunning returns whether the cleanup loop is active
func (a *Aggregator) IsRunning() bool {
    a.runningMu.Lock()
    defer a.runningMu.Unlock()
    return a.isRunning
}
```

### Benefits

1. ✅ **Prevents duplicate Start()** → Error returned
2. ✅ **Idempotent Stop()** → Safe to call multiple times
3. ✅ **Consistent with ircmanager** → Team familiarity
4. ✅ **Better error handling** → Clear error messages

---

## 3. 🟢 MEDIUM PRIORITY: `internal/diskmon/monitor.go` - Monitor (Future-Proofing)

### Current Status

**No background goroutines currently.**  
`Monitor` performs checks on-demand via `Check()` method.

### When to Apply

**IF** auto-polling feature is added in the future:

```go
type Monitor struct {
    mu sync.RWMutex
    
    path      string
    threshold int64
    checkFn   func(path string) (available, total int64, err error)
    
    // State
    lowSpace    bool
    lastChecked time.Time
    interval    time.Duration
    
    // Lifecycle (if auto-polling added)
    ctx       context.Context
    cancel    context.CancelFunc
    wg        sync.WaitGroup
    isRunning bool
    runningMu sync.Mutex
    
    logger *log.Logger
}

func (m *Monitor) StartAutoCheck(interval time.Duration) error {
    m.runningMu.Lock()
    if m.isRunning {
        m.runningMu.Unlock()
        return fmt.Errorf("auto-check already running")
    }
    m.isRunning = true
    m.runningMu.Unlock()
    
    m.ctx, m.cancel = context.WithCancel(context.Background())
    m.wg.Add(1)
    go m.pollLoop(interval)
    
    return nil
}

func (m *Monitor) pollLoop(interval time.Duration) {
    defer m.wg.Done()
    defer func() {
        m.runningMu.Lock()
        m.isRunning = false
        m.runningMu.Unlock()
    }()
    
    ticker := time.NewTicker(interval)
    defer ticker.Stop()
    
    for {
        select {
        case <-m.ctx.Done():
            return
        case <-ticker.C:
            _, _, low, err := m.Check()
            if err != nil {
                m.logger.Printf("disk check error: %v", err)
            }
            
            m.mu.Lock()
            m.lowSpace = low
            m.mu.Unlock()
        }
    }
}

func (m *Monitor) StopAutoCheck() {
    if m.cancel != nil {
        m.cancel()
        m.wg.Wait()
    }
}
```

**Priority:** LOW (only if feature added)

---

## 4. 🟢 LOW PRIORITY: `internal/sse/hub.go` - Hub

### Current Status

**No background goroutines.**  
All operations are synchronous (Subscribe/Broadcast/Unsubscribe).

### When to Apply

**IF** background features are added:
- Event buffer cleanup goroutine
- Client timeout detection
- Metrics collection

**Current implementation is correct** for synchronous use case.

---

## 📝 Implementation Roadmap

### Phase 1: High Priority (Next Sprint)
- [ ] **QueueManager**: Apply WaitGroup to `monitorLoop`
- [ ] **QueueManager**: Unify disk monitor with WaitGroup
- [ ] **Tests**: Add lifecycle tests (duplicate Start, clean shutdown)
- [ ] **Documentation**: Update package docs

**Estimated Effort:** 3-4 hours  
**Risk:** Low (similar to ircmanager fix)

### Phase 2: Medium Priority (Following Sprint)
- [ ] **Aggregator**: Apply WaitGroup to `cleanupLoop`
- [ ] **Tests**: Add lifecycle tests
- [ ] **Code Review**: Ensure consistency across codebase

**Estimated Effort:** 1-2 hours  
**Risk:** Very Low

### Phase 3: Future (As Needed)
- [ ] **Monitor**: Only if auto-polling added
- [ ] **Hub**: Only if background workers added

---

## 🎓 Pattern Template for Future Components

Use this template when creating new components with background goroutines:

```go
type Component struct {
    // Dependencies
    store  store.Store
    logger *log.Logger
    
    // Lifecycle management (WaitGroup pattern)
    ctx          context.Context
    cancel       context.CancelFunc
    wg           sync.WaitGroup
    isRunning    bool
    runningMu    sync.Mutex
    shutdownOnce sync.Once
    
    // State (protected by mu)
    mu    sync.RWMutex
    data  map[string]interface{}
}

// Start begins background processing
func (c *Component) Start(ctx context.Context) error {
    // Prevent duplicate calls
    c.runningMu.Lock()
    if c.isRunning {
        c.runningMu.Unlock()
        return fmt.Errorf("component already started")
    }
    c.isRunning = true
    c.runningMu.Unlock()
    
    // Initialize context
    c.ctx, c.cancel = context.WithCancel(ctx)
    
    // Start background worker(s)
    c.wg.Add(1)
    go c.worker()
    
    return nil
}

// Stop halts background processing
func (c *Component) Stop() {
    // Ensure idempotent shutdown
    c.shutdownOnce.Do(func() {
        if c.cancel != nil {
            c.cancel()
        }
        c.wg.Wait()
    })
}

// worker is the main background goroutine
func (c *Component) worker() {
    defer c.wg.Done()
    defer func() {
        c.runningMu.Lock()
        c.isRunning = false
        c.runningMu.Unlock()
        
        // Panic recovery
        if r := recover(); r != nil {
            c.logger.Printf("PANIC in worker: %v", r)
        }
    }()
    
    ticker := time.NewTicker(30 * time.Second)
    defer ticker.Stop()
    
    for {
        select {
        case <-c.ctx.Done():
            return
        case <-ticker.C:
            c.doWork()
        }
    }
}

// IsRunning returns whether the component is active
func (c *Component) IsRunning() bool {
    c.runningMu.Lock()
    defer c.runningMu.Unlock()
    return c.isRunning
}
```

---

## 🔗 Related Documents

- [agent.md - Goroutine Lifecycle Management](../../agent.md#goroutine-lifecycle-management)
- [Race Condition Fix Summary](../../.github/RACE_FIX_SUMMARY.md)
- [ircmanager Implementation](../../internal/ircmanager/manager.go)

---

## 📊 Success Metrics

After implementing WaitGroup pattern across components:

1. ✅ **Zero race conditions** in `go test -race ./...`
2. ✅ **Idempotent Start/Stop** → No panics on duplicate calls
3. ✅ **Deterministic shutdown** → No timeout warnings in logs
4. ✅ **Improved observability** → `IsRunning()` available for all components
5. ✅ **Team adoption** → Pattern becomes standard for new components

---

**Last Updated:** 2026-05-22  
**Author:** AI Assistant (GitHub Copilot)  
**Reviewer:** To be assigned
