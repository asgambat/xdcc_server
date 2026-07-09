# Guida al profiling con pprof per xdcc_server

Guida completa all'uso di `go tool pprof` per profilare CPU, heap, goroutine e leggere i flame graph nel server xdcc_server.

## Setup: endpoint e admin token

Gli endpoint pprof sono stati aggiunti al router in `internal/api/router.go` e sono protetti da `X-Admin-Token` (gruppo `RequireAdminToken`).

Poiché `go tool pprof` non supporta header HTTP custom, il workflow è in due step:

```bash
# 1. Scarica il profilo con curl
ADMIN_TOKEN="il-tuo-token"
BASE="http://localhost:8080"

# CPU (campiona per 30 secondi)
curl -H "X-Admin-Token: $ADMIN_TOKEN" -o cpu.pprof "$BASE/debug/pprof/profile?seconds=30"

# Heap (istantanea della memoria in uso)
curl -H "X-Admin-Token: $ADMIN_TOKEN" -o heap.pprof "$BASE/debug/pprof/heap"

# Goroutine
curl -H "X-Admin-Token: $ADMIN_TOKEN" -o goroutine.pprof "$BASE/debug/pprof/goroutine"

# Allocs (memoria totale allocata da inizio programma — più utile di heap per trovare leak)
curl -H "X-Admin-Token: $ADMIN_TOKEN" -o allocs.pprof "$BASE/debug/pprof/allocs"

# Block (goroutine bloccate su mutex/canali)
curl -H "X-Admin-Token: $ADMIN_TOKEN" -o block.pprof "$BASE/debug/pprof/block"

# Mutex (contesa mutex)
curl -H "X-Admin-Token: $ADMIN_TOKEN" -o mutex.pprof "$BASE/debug/pprof/mutex"

# Execution trace
curl -H "X-Admin-Token: $ADMIN_TOKEN" -o trace.out "$BASE/debug/pprof/trace?seconds=5"

# 2. Analizza con go tool pprof
go tool pprof cpu.pprof
```

> **Nota**: `block` e `mutex` richiedono di aver impostato i relativi rate nel runtime. Per abilitarli:
> ```go
> runtime.SetBlockProfileRate(1)
> runtime.SetMutexProfileFraction(1)
> ```

---

## CPU Profiling

Il CPU profiling campiona lo stack trace a intervalli regolari (default 100Hz) e indica **dove il programma sta spendendo tempo CPU**.

### Comandi interattivi essenziali

```
(pprof) top10          # Le 10 funzioni dove la CPU spende più tempo
(pprof) top10 -cum     # Ordinato per tempo cumulativo (inclusi i chiamati)
(pprof) list funcName  # Mostra il codice sorgente con annotazioni riga-per-riga
(pprof) web            # Apre il flame graph nel browser (richiede graphviz)
(pprof) peek funcName  # Chi chiama questa funzione e chi viene chiamato
(pprof) tree           # Albero delle chiamate
```

### Significato delle colonne

| Colonna | Significato |
|---------|-------------|
| `flat`  | Tempo CPU speso **direttamente** nella funzione |
| `flat%` | Percentuale sul totale del tempo CPU |
| `sum%`  | Percentuale cumulativa (scorrendo `top`) |
| `cum`   | Tempo CPU speso nella funzione **più tutto ciò che chiama** |
| `cum%`  | Percentuale cumulativa |

### Interpretazione

- **`flat%` alta** → la funzione stessa è il collo di bottiglia (es. parsing, marshalling, calcoli)
- **`flat%` bassa ma `cum%` alta** → la funzione è un orchestratore; il problema è a valle nei chiamati

---

## Heap Profiling

Misura la memoria **attualmente in uso** (oggetti vivi). **Non** include la memoria già liberata dal GC.

### Tipi di heap

```bash
# heap in-use — memoria attualmente allocata e non ancora liberata
go tool pprof -http=:6060 heap.pprof

# allocs — memoria totale allocata dall'inizio del programma (per trovare leak!)
go tool pprof -http=:6060 allocs.pprof
```

### Comandi nel REPL

```
(pprof) top10          # 10 allocazioni più pesanti in-use
(pprof) top10 -cum     # Inclusi i chiamanti
(pprof) list funcName  # Sorgente annotato con allocazioni per riga
(pprof) inuse_space    # Mostra in MB (default) — memoria attuale
(pprof) alloc_space    # Mostra in MB — memoria allocata totale (per leak)
(pprof) inuse_objects  # Mostra conteggio oggetti — memoria attuale
(pprof) alloc_objects  # Mostra conteggio oggetti — totale allocato
```

### Cosa cercare

- **`inuse_space`** grande + **stabile** = il server usa tanta memoria, ok se voluto
- **`alloc_space`** che cresce continuamente + **`inuse_space`** stabile = molte allocazioni ma GC efficiente
- **`alloc_space`** che cresce + **`inuse_space`** che cresce = **memory leak probabile!**

### Pattern tipici di leak in Go

- Goroutine che non terminano (trattengono riferimenti a oggetti)
- Mappe (`map`) che crescono senza pulizia
- Channel non chiusi che bloccano goroutine (e la loro memoria)
- Slice che referenziano backing array molto grandi

Nel progetto xdcc_server, occhio a:
- `searchagg/cache.go` — cache senza TTL o senza eviction
- `sse/hub.go` — buffer eventi che crescono se i consumer sono lenti
- `ircmanager/` — connessioni IRC non chiuse
- `download/dcc.go` — buffer DCC non rilasciati

---

## Goroutine Profiling

Mostra **tutte** le goroutine attualmente in esecuzione, cosa stanno facendo e dove sono bloccate.

```bash
go tool pprof -http=:6060 goroutine.pprof
```

### Cosa guardare

Un numero **alto e crescente** di goroutine bloccate sulla stessa funzione = **goroutine leak**.

### Pattern comuni da cercare

| Funzione runtime | Significato |
|------------------|-------------|
| `runtime.gopark` | Goroutine parcheggiata (normale per `select{}`, `time.Sleep`) |
| `runtime.chanrecv` | In attesa di ricevere da un canale |
| `runtime.chansend` | Bloccata in invio su un canale |
| `runtime.selectgo` | In attesa in uno statement `select` |
| `syscall.Syscall6` | Chiamata di sistema bloccante (tipico I/O di rete) |
| `runtime.notetsleepg` | In attesa su una nota/condizione interna |

### Dump testuale completo

Oltre a pprof, xdcc_server ha un endpoint dedicato per il dump testuale di tutte le goroutine (protetto da admin token):

```bash
curl -H "X-Admin-Token: $ADMIN_TOKEN" http://localhost:8080/debug/goroutines/dump
```

Questo restituisce il dump completo (`pprof.Lookup("goroutine").WriteTo(w, 2)`) — equivalente a un `SIGQUIT` mandato al processo, ma via HTTP.

---

## Leggere i Flame Graph

Il flame graph è la visualizzazione più potente per capire **dove** il programma spende risorse.

### Anatomia

```
 ┌──────────────────────────────────────────────────────────────┐
 │                         main                                 │  ← root (base)
 ├──────────────────────┬───────────────────────┬───────────────┤
 │     runServer        │     ServeHTTP          │   altraFn     │  ← livello 1
 ├──────┬───────┬───────┼──────┬───────┬───────┼───────────────┤
 │ init │ start │ serve │ read │parse  │ write │               │  ← livello 2
 ├──────┴──┬────┴───────┼──────┴───────┼───────┤               │
 │ syscall │ dbQuery     │ json.Unmarsh │ alloc │               │  ← foglie
 └─────────┴─────────────┴──────────────┴───────┴───────────────┘
```

**Regola d'oro**: la **larghezza** di ogni rettangolo è proporzionale alla risorsa consumata. Più è largo, più è pesante. L'asse Y è lo stack di chiamate (in alto = più profondo nello stack).

### Come navigare (`-http=:6060`)

```bash
go tool pprof -http=:6060 cpu.pprof
# Apri http://localhost:6060 nel browser
```

| Azione | Significato |
|--------|-------------|
| **Clicca un rettangolo** | Zoomma su quella funzione e tutto il suo sottoalbero |
| **Clicca "Reset"** | Torna alla vista completa |
| **Passa il mouse** | Vedrai: nome funzione, `flat`, `cum`, percentuali |
| **Barra di ricerca in alto** | Filtra per nome funzione o package (es. `xdcc_server`, `runtime`) |
| **Menu View → Flame Graph** | Vista standard — quadro generale |
| **Menu View → Top** | Lista testuale — identificare numericamente i colpevoli |
| **Menu View → Source** | Codice sorgente con annotazioni riga-per-riga |
| **Menu Sample** | Cambia metrica (`inuse_space` vs `alloc_space`, `samples` vs `seconds`) |

### Colori

I colori in pprof non hanno significato intrinseco: sono assegnati per distinguere package diversi. Funzioni dello stesso package hanno colori simili.

Pattern di riconoscimento rapido:
- **`runtime/*`** (GC, scheduler) — scorciatoie del runtime
- **`syscall`** — chiamate di sistema, spesso I/O — il tempo qui è "fuori dal tuo controllo"
- **Il tuo codice** (`xdcc_server/**`) — il resto

### Pattern da cercare

1. **Torre larga in cima (wide flat top)**: una singola funzione foglia domina il profilo → ottimizzala
   ```
   [███████████████████████████ json.Unmarshal]  ← 60% del tempo!
   ```

2. **Molti piccoli rettangoli sparsi**: tempo frammentato → difficile da ottimizzare, il codice è già ben distribuito

3. **Piattaforme larghe alla base**: un orchestratore chiama molte sotto-funzioni → guarda i chiamati per capire dove intervenire
   ```
   [███████████████████████████ DownloadAll █████████████████████]
    ├──[████ DCC transfer ████]
    └──[███████ logging ███████]
   ```

4. **`runtime.mallocgc` / `runtime.gcBgMarkWorker`** molto larghi → il GC è sotto pressione, stai allocando troppa memoria. Riduci le allocazioni.

### Esempio: analisi CPU

```bash
go tool pprof -http=:6060 cpu.pprof
```

Nel flame graph CPU, pattern tipici:
- **Strette torri verticali** di `runtime.mallocgc` → troppe allocazioni
- **Grandi blocchi di `syscall.Read` / `syscall.Write`** → tempo in I/O rete (normale per server IRC/download)
- **Blocchi larghi di `encoding/json`** → parsing JSON pesante (risposte API, config)

### Esempio: analisi heap

```bash
go tool pprof -http=:6060 heap.pprof
```

Passa a **View → Flame Graph** e seleziona `inuse_space` o `alloc_space` nel menu Sample:
- Rettangolo largo di `sqlite3` → il DB tiene memoria in cache (probabilmente normale)
- Rettangolo largo di `sse.Hub` / `LogBroadcaster` → buffer eventi SSE grandi
- Rettangolo di `searchagg.Cache` che cresce → cache senza eviction

---

## Comandi avanzati

### Diff tra due snapshot (per trovare leak)

```bash
# Prendi due snapshot separati nel tempo
curl -H "X-Admin-Token: $TOKEN" -o heap1.pprof "$BASE/debug/pprof/heap"
sleep 60
curl -H "X-Admin-Token: $TOKEN" -o heap2.pprof "$BASE/debug/pprof/heap"

# Mostra SOLO la memoria allocata nel periodo tra i due snapshot
go tool pprof -http=:6060 -base=heap1.pprof heap2.pprof
# Se vedi qualcosa che cresce → leak!
```

### Confronto CPU

```bash
go tool pprof -http=:6060 -base=cpu_before.pprof cpu_after.pprof
```

### Output SVG statico

```bash
go tool pprof -svg cpu.pprof > flame.svg
```

### Filtrare per package

```bash
# Mostra solo il tuo codice
go tool pprof -hide=runtime -focus=xdcc_server cpu.pprof

# Nascondi funzioni specifiche
go tool pprof -hide=runtime -hide=syscall cpu.pprof
```

### Profilare direttamente da remoto (se accessibile)

Se il server è raggiungibile senza token (es. in dev con bind su `0.0.0.0` e senza admin token), puoi usare `go tool pprof` direttamente:

```bash
go tool pprof -http=:6060 http://localhost:8080/debug/pprof/profile?seconds=30
go tool pprof -http=:6060 http://localhost:8080/debug/pprof/heap
```

---

## Checklist per xdcc_server

In base all'architettura del progetto, ecco cosa profilare per ogni tipo di problema:

| Problema sospetto | Profilo | Cosa cercare |
|---|---|---|
| Server lento a rispondere | CPU | `json.Marshal`/`Unmarshal`, query SQL lente |
| Memoria che cresce nel tempo | `allocs` + diff | Mappe senza cleanup, goroutine leak |
| Handler SSE che si bloccano | `goroutine` | `chansend`/`chanrecv` in `sse/hub.go` |
| Download lenti | CPU | Tempo in `dcc.go`, `syscall` I/O |
| IRC disconnect frequenti | `goroutine` | Goroutine `ircmanager` bloccate su canali |
| Watchlist consuma risorse | CPU + goroutine | `searchagg/watchlist.go` |
| Goroutine in crescita | `goroutine` + dump | Stack ripetuti della stessa funzione |

### File chiave da tenere d'occhio

| File | Rischio | Profilo |
|------|---------|---------|
| `internal/sse/hub.go` | Buffer eventi, goroutine subscriber | goroutine, heap |
| `internal/queue/worker.go` | Download worker pool, goroutine leak | goroutine |
| `internal/ircmanager/manager.go` | Connessioni IRC, auto-reconnect | goroutine, CPU |
| `internal/searchagg/cache.go` | Cache senza eviction | heap, allocs |
| `internal/store/sqlite.go` | Query lente, memory SQLite | CPU, heap |
| `internal/logging/broadcaster.go` | Buffer log in memoria | heap |
| `internal/downloader/downloader.go` | Buffer DCC, connection leak | goroutine, heap |

---

## Riferimenti

- [Go pprof documentation](https://pkg.go.dev/net/http/pprof)
- [Google pprof GitHub](https://github.com/google/pprof)
- [Go execution tracer](https://pkg.go.dev/runtime/trace)
- [Julia Evans — Profiling Go programs with pprof](https://jvns.ca/blog/2017/09/24/profiling-go-with-pprof/)
- [Uber Go Style Guide — Performance](https://github.com/uber-go/guide/blob/master/style.md#performance)
