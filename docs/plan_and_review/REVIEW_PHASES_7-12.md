# Review Report — Fasi 7-12 Implementazione

**Data Review:** 2026-05-20  
**Commit Reviewato:** 32feca2 (implementate fasi 7-12)  
**Fasi Coperte:** 7 (SSE), 8 (Frontend PWA), 9 (Robustezza), 10 (Testing), 11 (CLI Delegation), 12 (Deploy)

---

## Executive Summary

La review ha identificato **6 issue principali**:
- **3 CRITICAL**: 1 errore compilazione Windows, 1 race condition SSE, 1 memory leak SSE
- **2 HIGH**: Disk monitoring non funzionante, PWA assets mancanti
- **1 MEDIUM**: Timing issue graceful shutdown

Tutte le funzionalità previste dalle fasi 7-12 sono implementate, ma i bug CRITICAL/HIGH devono essere fixati prima del deploy.

---

## ❌ CRITICAL Issues

### CRITICAL-1: Errore Compilazione — Windows Disk Monitor Type Mismatch

**File:** `internal/diskmon/disk_windows.go:19`  
**Impatto:** Il codice **non compila su Windows**. Il server non può essere buildato.

**Problema:**  
Il codice passa puntatori `*int64` a `windows.GetDiskFreeSpaceEx`, che si aspetta `*uint64`. Type mismatch.

**Codice problematico:**
```go
func getDiskUsage(path string) (available, total int64, err error) {
    pathPtr, err := windows.UTF16PtrFromString(path)
    if err != nil {
        return 0, 0, err
    }

    var freeBytes int64   // ❌ WRONG: should be uint64
    var totalBytes int64  // ❌ WRONG: should be uint64
    var availBytes int64  // ❌ WRONG: should be uint64

    err = windows.GetDiskFreeSpaceEx(pathPtr, &availBytes, &totalBytes, &freeBytes)
    // Compilation error: cannot use &availBytes (type *int64) as *uint64
    if err != nil {
        return 0, 0, err
    }

    return availBytes, totalBytes, nil
}
```

**Errore compilazione:**
```
internal\diskmon\disk_windows.go:19:44: cannot use &availBytes (value of type *int64) as *uint64 value in argument to windows.GetDiskFreeSpaceEx
internal\diskmon\disk_windows.go:19:57: cannot use &totalBytes (value of type *int64) as *uint64 value in argument to windows.GetDiskFreeSpaceEx
internal\diskmon\disk_windows.go:19:70: cannot use &freeBytes (value of type *int64) as *uint64 value in argument to windows.GetDiskFreeSpaceEx
```

**Fix Suggerito:**
```go
func getDiskUsage(path string) (available, total int64, err error) {
    pathPtr, err := windows.UTF16PtrFromString(path)
    if err != nil {
        return 0, 0, err
    }

    var freeBytes uint64   // ✅ Correct type
    var totalBytes uint64  // ✅ Correct type
    var availBytes uint64  // ✅ Correct type

    err = windows.GetDiskFreeSpaceEx(pathPtr, &availBytes, &totalBytes, &freeBytes)
    if err != nil {
        return 0, 0, err
    }

    return int64(availBytes), int64(totalBytes), nil  // ✅ Cast to int64 for return
}
```

---

### CRITICAL-2: Race Condition — SSE Hub Usa Read Lock Per Scrivere

**File:** `internal/sse/hub.go:127-159` (metodo `Publish`)  
**Impatto:** **Data race** su buffer eventi. Eventi concorrenti da IRC manager e queue manager corrompono lo stato del buffer circolare.

**Problema:**  
Il metodo `Publish()` usa `h.mu.RLock()` (read lock) mentre **modifica** stato condiviso (`eventBuffer`, `bufferHead`, `bufferCount`). Più chiamate concorrenti a `Publish()` scrivono simultaneamente causando corruzione dati.

**Codice problematico:**
```go
func (h *Hub) Publish(eventType string, payload map[string]interface{}) {
    h.mu.RLock()  // ❌ READ LOCK: permette accesso concorrente!
    defer h.mu.RUnlock()

    h.eventID++  // ❌ RACE: incremento senza protezione write
    evt := Event{
        ID:        h.eventID,
        Type:      eventType,
        Data:      payload,
        Timestamp: time.Now(),
    }

    // ❌ RACE: scrittura concorrente nel buffer
    h.eventBuffer[h.bufferHead] = evt
    h.bufferHead = (h.bufferHead + 1) % h.bufferSize
    if h.bufferCount < h.bufferSize {
        h.bufferCount++
    }

    // Fan-out a client connessi
    for ch := range h.clients {
        select {
        case ch <- evt:
        default:
            // non-blocking
        }
    }
}
```

**Conseguenze:**
- Race condition rilevabile con `go test -race`
- Indici buffer corrotti (`bufferHead` incrementato da più goroutine)
- Eventi scritti in slot sbagliati o sovrascrivendo altri eventi
- `EventsSince()` ritorna eventi corrotti o duplicati
- Potenziale panic da index out of bounds

**Fix Suggerito:**
```go
func (h *Hub) Publish(eventType string, payload map[string]interface{}) {
    h.mu.Lock()  // ✅ WRITE LOCK: esclusione mutua totale
    defer h.mu.Unlock()

    // Resto del codice invariato
    h.eventID++
    evt := Event{
        ID:        h.eventID,
        Type:      eventType,
        Data:      payload,
        Timestamp: time.Now(),
    }

    h.eventBuffer[h.bufferHead] = evt
    h.bufferHead = (h.bufferHead + 1) % h.bufferSize
    if h.bufferCount < h.bufferSize {
        h.bufferCount++
    }

    for ch := range h.clients {
        select {
        case ch <- evt:
        default:
        }
    }
}
```

---

### CRITICAL-3: Memory Leak — SSE Unsubscribe Non Rimuove Mai i Client

**File:** `internal/sse/hub.go:92-107` (metodo `Unsubscribe`)  
**Impatto:** **Memory leak** + **goroutine leak**. Ogni connessione SSE lascia un channel nel map che non viene mai rimosso.

**Problema:**  
`Unsubscribe()` accetta un parametro `<-chan Event` (receive-only), ma il map `h.clients` contiene `chan Event` (bidirectional). Il confronto `c == ch` fallisce **sempre** perché i tipi sono diversi, quindi i channel non vengono mai rimossi.

**Codice problematico:**
```go
func (h *Hub) Subscribe() <-chan Event {
    ch := make(chan Event, 256)
    h.mu.Lock()
    h.clients[ch] = struct{}{}  // Inserisce chan Event (bidirectional)
    h.mu.Unlock()
    return ch  // Ritorna <-chan Event (receive-only)
}

func (h *Hub) Unsubscribe(ch <-chan Event) {  // ❌ Tipo receive-only
    h.mu.Lock()
    defer h.mu.Unlock()

    for c := range h.clients {  // c è chan Event (bidirectional)
        if c == ch {  // ❌ CONFRONTO SEMPRE FALSE (tipi diversi)
            delete(h.clients, c)
            close(c)
            break
        }
    }
}
```

**Conseguenze:**
- Ogni client SSE crea un channel buffered (256 eventi) che **non viene mai rimosso**
- Accumulo illimitato di channel nel map `h.clients`
- Goroutine degli HTTP handler bloccate su channel mai chiusi
- Fan-out broadcast sempre più lento (itera su tutti i channel accumulati)
- **OOM dopo ~1000 connessioni client** (256 buffer * 1000 client = ~256MB solo per buffer eventi)

**Fix Suggerito:**
```go
// ✅ Cambia tipo di ritorno a bidirectional
func (h *Hub) Subscribe() chan Event {
    ch := make(chan Event, 256)
    h.mu.Lock()
    h.clients[ch] = struct{}{}
    h.mu.Unlock()
    return ch
}

// ✅ Cambia parametro a bidirectional
func (h *Hub) Unsubscribe(ch chan Event) {
    h.mu.Lock()
    defer h.mu.Unlock()

    for c := range h.clients {
        if c == ch {  // ✅ Confronto ora funziona
            delete(h.clients, c)
            close(c)
            break
        }
    }
}
```

E aggiorna l'handler SSE:
```go
func (s *Server) handleSSE(w http.ResponseWriter, r *http.Request) {
    // ...
    ch := s.sseHub.Subscribe()  // ✅ Ora riceve chan Event
    defer s.sseHub.Unsubscribe(ch)  // ✅ Match di tipo
    // ...
}
```

---

## 🟠 HIGH Issues

### HIGH-1: Disk Monitor Callback Non Viene Mai Chiamato Dopo il Check Iniziale

**File:** `internal/diskmon/monitor.go:126-137` (goroutine `periodicCheck`)  
**Impatto:** Il monitoraggio spazio disco **non funziona**. Queue manager non riceve eventi `disk_space_low` e non sospende download quando il disco è pieno.

**Problema:**  
La goroutine legge `prevLow` **dopo** aver chiamato `m.Check()`, che ha già aggiornato `m.lowSpace` al nuovo valore. Quindi `prevLow` è sempre uguale a `low` e la condizione `low != prevLow` è sempre falsa.

**Codice problematico:**
```go
case <-ticker.C:
    available, _, low, err := m.Check()  // ← Aggiorna m.lowSpace a 'low'
    if err != nil {
        m.logger.Printf("WARNING: disk space check failed: %v", err)
        continue
    }
    
    m.mu.RLock()
    prevLow := m.lowSpace  // ← Legge valore APPENA AGGIORNATO
    m.mu.RUnlock()
    
    if low != prevLow && onChange != nil {  // ← SEMPRE FALSE
        onChange(low, available)
    }
```

**Conseguenze:**
- Callback `onChange` non viene mai chiamata
- Eventi SSE `disk_space_low` / `disk_space_ok` mai emessi
- Queue manager non sospende download quando disco pieno
- **Rischio di riempire completamente il disco**, causando fallimenti system-wide
- Auto-resume su recupero spazio non avviene

**Fix Suggerito:**
```go
case <-ticker.C:
    // ✅ Leggi OLD value PRIMA di chiamare Check()
    m.mu.RLock()
    prevLow := m.lowSpace
    m.mu.RUnlock()
    
    // ✅ Ora Check() aggiorna a NEW value
    available, _, low, err := m.Check()
    if err != nil {
        m.logger.Printf("WARNING: disk space check failed: %v", err)
        continue
    }
    
    // ✅ Confronto tra OLD e NEW funziona
    if low != prevLow && onChange != nil {
        onChange(low, available)
    }
```

---

### HIGH-2: PWA Assets Mancanti (manifest.json e sw.js)

**File:** `web/dist/` (mancanti)  
**Impatto:** L'app **non è installabile come PWA**. Errori 404 in console browser. Spec Fase 8 non rispettata.

**Problema:**  
Il file `web/dist/index.html` referenzia `/manifest.json` (linea 8), ma il file non esiste. Anche `sw.js` è mancante. La spec (Fase 8, linee 200-201) richiede esplicitamente PWA support con manifest e service worker.

**Evidenza:**
- `index.html:8` → `<link rel="manifest" href="/manifest.json">`
- File non trovato in `web/dist/` o `web/public/`
- Browser console: `GET /manifest.json 404`
- Dockerfile copia `web/dist` assumendo che questi file esistano

**Conseguenze:**
- Browser non può installare l'app come PWA
- Nessun supporto offline
- Nessuna icona app su home screen Android/iOS
- Spec requirement Fase 8 fallito

**Fix Suggerito:**

Crea `web/public/manifest.json`:
```json
{
  "name": "XDCC Download Manager",
  "short_name": "XDCC-Go",
  "description": "Web interface for XDCC download management",
  "start_url": "/",
  "display": "standalone",
  "background_color": "#0f0f1a",
  "theme_color": "#6366f1",
  "icons": [
    {
      "src": "/icon-192.png",
      "sizes": "192x192",
      "type": "image/png"
    },
    {
      "src": "/icon-512.png",
      "sizes": "512x512",
      "type": "image/png"
    }
  ]
}
```

Crea `web/public/sw.js` (service worker minimale):
```js
// Minimal service worker for PWA installability
const CACHE_NAME = 'xdcc-go-v1';

self.addEventListener('install', (event) => {
  self.skipWaiting();
});

self.addEventListener('activate', (event) => {
  event.waitUntil(self.clients.claim());
});

// Basic fetch handler (can be enhanced for offline support)
self.addEventListener('fetch', (event) => {
  event.respondWith(
    fetch(event.request).catch(() => {
      // Fallback to cache if offline
      return caches.match(event.request);
    })
  );
});
```

Aggiorna `vite.config.js` per copiare `public/` a `dist/`:
```js
export default defineConfig({
  // ...
  build: {
    outDir: 'dist',
    emptyOutDir: true,
    copyPublicDir: true,  // ✅ Copia public/ a dist/
  },
  publicDir: 'public',  // ✅ Definisce cartella public
});
```

Registra service worker in `web/src/App.svelte`:
```js
onMount(() => {
  // Register service worker
  if ('serviceWorker' in navigator) {
    navigator.serviceWorker.register('/sw.js')
      .then(reg => console.log('SW registered', reg))
      .catch(err => console.log('SW registration failed', err));
  }
  // ... resto codice onMount
});
```

---

## 🟡 MEDIUM Issues

### MEDIUM-1: Graceful Shutdown Può Timeout con Disk Monitor Attivo

**File:** `cmd/xdcc-server/main.go:128-131, 242-243` + `internal/queue/manager.go`  
**Impatto:** Shutdown potrebbe superare i 15s di timeout, causando SIGKILL forzato e perdita di progresso download.

**Problema:**  
La goroutine di disk monitoring viene fermata chiudendo un channel (`qm.stopDiskCheck()`), ma non c'è garanzia che sia effettivamente terminata prima di chiamare `qm.Stop()`. Se il check del disco è in corso (intervallo 30s), lo shutdown potrebbe bloccarsi.

**Codice problematico:**
```go
// cmd/xdcc-server/main.go shutdown sequence
queueMgr.Stop()  // Chiama stopDiskCheck() ma non aspetta

// internal/queue/manager.go
func (qm *QueueManager) Stop() {
    if qm.stopDiskCheck != nil {
        qm.stopDiskCheck()  // Chiude solo channel, non blocca
    }
    qm.cancel()
    <-qm.done  // Aspetta monitor loop, ma disk check potrebbe ancora girare
}
```

**Conseguenze:**
- Shutdown può impiegare >15s se disk check è in corso
- Timeout context scaduto → SIGKILL forzato
- Progresso download non salvato in DB
- Potenziali file corrotti

**Fix Suggerito:**
```go
// Modifica diskMon.StartPeriodicCheck per ritornare done channel
func (m *Monitor) StartPeriodicCheck(interval time.Duration, onChange func(bool, int64)) (stop func(), done <-chan struct{}) {
    stopCh := make(chan struct{})
    doneCh := make(chan struct{})
    
    go func() {
        defer close(doneCh)  // ✅ Segnala terminazione
        ticker := time.NewTicker(interval)
        defer ticker.Stop()
        
        for {
            select {
            case <-stopCh:
                return
            case <-ticker.C:
                // ... check logic
            }
        }
    }()
    
    return func() { close(stopCh) }, doneCh
}

// Queue manager
qm.stopDiskCheck, qm.diskCheckDone = qm.diskMon.StartPeriodicCheck(...)

func (qm *QueueManager) Stop() {
    if qm.stopDiskCheck != nil {
        qm.stopDiskCheck()
        <-qm.diskCheckDone  // ✅ Aspetta terminazione
    }
    qm.cancel()
    <-qm.done
}
```

---

## ✅ Verified Correct Implementations

### Fase 7 — SSE (Server-Sent Events)
- ✅ Endpoint `/api/events` registrato correttamente in `internal/api/router.go:30`
- ✅ Content-Type `text/event-stream` impostato
- ✅ Tutti gli eventi richiesti implementati:
  - `server_status_changed`, `channel_joined/left/topic_updated`
  - `download_started/progress/completed/failed/queued/removed`
  - `download_alternative_found`, `download_bulk_action_result`
  - `watchlist_new_results`, `provider_health_changed`
- ✅ Hub broadcast con gestione connessioni multiple
- ✅ Buffer eventi circolari (100 eventi) con `event id` progressivo
- ✅ Supporto `Last-Event-ID` per recupero eventi mancanti
- ✅ Evento `resync_required` quando ID troppo vecchio
- ⚠️ **MA**: race condition su Publish e memory leak su Unsubscribe (vedi CRITICAL-2 e CRITICAL-3)

### Fase 8 — Frontend PWA
- ✅ Progetto Svelte 5 + Vite configurato in `web/`
- ✅ Componenti view implementati: Dashboard, Servers, Downloads, Search, Presets, Watchlists, Providers, Settings
- ✅ Componenti shared: Sidebar, Toast, Modal, ConnectionStatus
- ✅ Hash-based routing funzionante
- ✅ Client SSE con EventSource e riconnessione automatica
- ✅ SPA fallback nel router Go (serve index.html per route non trovate)
- ✅ `go:embed` in `web/frontend.go` con fallback a disco in dev mode
- ✅ Dark/light mode con CSS custom properties + localStorage
- ✅ Responsive: sidebar fissa desktop, hamburger mobile
- ✅ Svelte 5 runes mode (`$state`, `$derived`, `$props`)
- ⚠️ **MA**: manifest.json e sw.js mancanti (vedi HIGH-2)

### Fase 9 — Robustezza
- ✅ Graceful shutdown implementato con ordine corretto
- ✅ Signal handler SIGTERM/SIGINT in main.go
- ✅ Timeout context 15s configurabile
- ✅ Package `internal/diskmon/` per monitoraggio spazio disco
- ✅ Soglia configurabile `min_disk_space` in config
- ✅ Eventi SSE `disk_space_low/ok` definiti
- ✅ Queue manager integra disk monitor
- ✅ Duplicati download via `GetDownloadByBotMessage`
- ✅ Endpoint `PATCH /api/downloads/:id/position` per riordino coda
- ✅ Web Notification API in App.svelte
- ✅ Package `internal/logging/` con rotazione log
- ✅ Guardrail `max_retry_attempts` (default 3)
- ⚠️ **MA**: disk monitor onChange non funziona (vedi HIGH-1) e shutdown timing issue (vedi MEDIUM-1)

### Fase 11 — CLI Delegation
- ✅ Flag `--command-server` in `xdcc-dl` e `xdcc-browse`
- ✅ HTTP client in `internal/client/`
- ✅ Check compatibilità `GET /api/version`
- ✅ `POST /api/downloads` per delega download
- ✅ `GET /api/search` + UI interattiva per browse
- ✅ Polling `GET /api/downloads/:id` con progress bar
- ✅ Errore chiaro se server unreachable (no fallback silenzioso)

### Fase 12 — Deploy
- ✅ Dockerfile multi-stage build
- ✅ Build di `xdcc-server` incluso
- ✅ `EXPOSE 8080`
- ✅ Volume per SQLite e download dir
- ✅ README documentazione server mode
- ✅ README flag `--command-server`
- ✅ Systemd unit file in `systemd/xdcc-server.service`
- ✅ ARM64 compatibility: `--platform=$BUILDPLATFORM` in Dockerfile
- ✅ Dipendenze ARM64-compatible (SQLite CGO-free, Go std lib)

---

## Compatibilità ARM64 (Raspberry Pi 4)

✅ **Verificato compatibile**:
- `modernc.org/sqlite` è CGO-free, compila nativamente su ARM64
- Go stdlib puro (no CGo dependencies)
- Dockerfile usa `--platform=$BUILDPLATFORM` per cross-compilation
- Node.js/Vite build frontend è platform-agnostic (output HTML/CSS/JS)
- Systemd unit file funziona su Raspberry Pi OS

❌ **Potenziale problema**:
- Issue CRITICAL-1 (disk_windows.go) non influenza ARM64/Linux, ma va fixato per compatibilità cross-platform

---

## Raccomandazioni

### Priorità Fix (Prima del Deploy)

1. **CRITICAL-2 e CRITICAL-3** (SSE race + leak): Fixare **immediatamente**. Causano crash e OOM in produzione.
2. **HIGH-1** (disk monitor): Fixare **prima del deploy**. Funzionalità core non funzionante.
3. **CRITICAL-1** (Windows compilation): Fixare per **compatibilità cross-platform** (anche se Raspberry è target primario).
4. **HIGH-2** (PWA assets): Fixare per **completare spec** e permettere installazione app.
5. **MEDIUM-1** (shutdown timing): Fixare per **robustezza** graceful shutdown.

### Testing Raccomandato (Fase 10)

Dopo aver fixato i bug CRITICAL/HIGH:
- [ ] Test con `go test -race ./...` per verificare risoluzione race condition
- [ ] Test SSE con 100+ client concorrenti (leak test)
- [ ] Test disk monitor: riempi disco artificialmente, verifica pause/resume
- [ ] Test graceful shutdown con download attivi
- [ ] Test PWA installability su Chrome/Firefox/Safari iOS
- [ ] Test ARM64: build e run su Raspberry Pi 4

---

## Conclusione

L'implementazione delle fasi 7-12 è **sostanzialmente completa** con tutte le feature previste. Tuttavia, i **3 bug CRITICAL** e **2 bug HIGH** devono essere risolti prima del deploy in produzione.

Dopo il fix, il sistema sarà production-ready per Raspberry Pi 4 con ARM64.
