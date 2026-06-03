# Goroutine Audit Report - Executive Summary

## 🚨 CRITICAL FINDINGS

### Root Cause of All Timeouts
**DEADLOCK between event loops and SSE handlers during shutdown:**

```
1. Signal received → Start shutdown
2. sseHub.Close() → Closes all SSE client channels
3. HTTP server.Shutdown() → Waits for handlers to exit
   BUT:
   - SSE handlers waiting on event channels
   - Event goroutines blocked in sseHub.Publish() (writing to closed channels)
   - No WaitGroup tracking → can't wait for event loops
   → DEADLOCK → 2m25s timeout!
```

---

## 🔴 CRITICAL Issues (Blocks Shutdown)

### 1. Event Channels Not Drained (main.go:187, 202)
**Severity:** CRITICAL  
**Priority:** 1  
**Impact:** 2m25s shutdown block

**Problem:**
```go
// Line 187-197: IRC events
ircEventCh := ircMgr.Subscribe()
defer ircMgr.Unsubscribe(ircEventCh)
go func() {
    for evt := range ircEventCh {
        sseHub.Publish(...)  // ← Blocks when hub closed!
    }
}()

// Line 200-217: Queue events (same pattern)
```

**Fix:** Track event loops in WaitGroup, stop BEFORE closing SSE hub

---

### 2. RunCleanup() No Completion Signal (store/recovery.go:204)
**Severity:** CRITICAL  
**Priority:** 2  
**Impact:** Database cleanup blocks 3s

**Problem:**
```go
// Current: returns only stop channel
func (s *Store) RunCleanup(...) (chan struct{}, error)

// Missing: done channel to signal completion
```

**Fix:** Return both stopCh and doneCh, wait for done during shutdown

---

### 3. SSE Handler Channel Race (api/handlers_sse.go:51)
**Severity:** CRITICAL  
**Priority:** 3  
**Impact:** SSE handlers block HTTP shutdown

**Problem:**
```go
ch := a.SSEHub.Subscribe()
defer a.SSEHub.Unsubscribe(ch)  // ← Called AFTER hub.Close()!

for {
    select {
    case evt, ok := <-ch:  // ← Channel already closed
        // ...
    }
}
```

**Fix:** Check if hub is closed before unsubscribing, add timeout to select

---

## 🟠 HIGH Severity (Causes Timeouts)

### 4. Queue Download Workers Not Tracked (queue/manager.go:549)
**Severity:** HIGH  
**Priority:** 4  
**Impact:** 10s timeout

**Problem:** Active download goroutines spawned without WaitGroup

**Fix:** Track all download workers, wait with timeout during stop

---

### 5. IRC run() Not Waited (ircmanager/manager.go:250)
**Severity:** HIGH  
**Priority:** 5  
**Impact:** 5s timeout

**Problem:** DisconnectServer() doesn't wait for run() to complete

**Fix:** Wait for mc.done channel in DisconnectServer()

---

## 📊 Systematic Issues

### Missing Patterns:
1. ❌ **WaitGroup tracking** for spawned goroutines
2. ❌ **Completion channels** (done) for background tasks
3. ❌ **Channel draining** during shutdown
4. ❌ **Context propagation** to nested goroutines
5. ❌ **Timeouts on blocking operations** (reconnect can sleep 1h!)

### Affected Components:
- **cmd/xdcc-server/main.go** - Event subscription loops
- **internal/api/handlers_sse.go** - SSE event handlers
- **internal/queue/manager.go** - Download workers
- **internal/ircmanager/manager.go** - Connection management
- **internal/store/recovery.go** - Cleanup goroutine
- **internal/irc/dcc.go** - DCC nested goroutines

---

## 🎯 Fix Priority Order

1. **Fix event loops in main.go** (CRITICAL - fixes deadlock)
2. **Fix RunCleanup completion signal** (CRITICAL - 3s improvement)
3. **Fix SSE handler race condition** (CRITICAL - enables HTTP shutdown)
4. **Add WaitGroup to queue downloads** (HIGH - 10s improvement)
5. **Wait for IRC run() completion** (HIGH - 5s improvement)

**Expected Result:** Shutdown <5s (from current 35s+)

---

## 📝 Implementation Plan

### Phase 1: Fix Event Loop Deadlock (1-2h)
- Add WaitGroup for event goroutines
- Stop event loops BEFORE closing SSE hub
- Add context checks in event loops

### Phase 2: Fix Component Stops (2-3h)
- RunCleanup return doneCh
- Queue downloads WaitGroup
- IRC run() wait for completion

### Phase 3: Refactor Stop() Signatures (Optional - 4-6h)
- All Stop() accept context.Context
- Uniform timeout handling
- No component-level timeouts

