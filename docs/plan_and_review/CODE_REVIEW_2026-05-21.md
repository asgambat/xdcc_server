# 🔍 Code Review Report — xdcc-go

**Data:** 2026-05-21  
**Ambito:** Analisi approfondita di concorrenza, bug, anomalie, manutenibilità e shutdown  
**File analizzati:** 12 file core (~2500+ LOC)

---

## 📊 Riepilogo

| Severità | Conteggio | Descrizione |
|----------|-----------|-------------|
| 🔴 CRITICAL | 4 | Crash, race condition, perdita dati |
| 🟠 HIGH | 4 | Bug funzionali, resource leak |
| 🟡 MEDIUM | 7 | Potenziali problemi, rischi |
| 🟢 LOW | 3 | Miglioramenti consigliati |

---

## 🔴 CRITICAL

### 1. `QueueManager.globalCount` — Double Decrement (Corruzione Contatore)

**File:** `internal/queue/manager.go` + `internal/queue/worker.go`  
**Categoria:** Concurrency Bug / Data Corruption

**Descrizione:** Quando un download viene cancellato tramite `CancelDownload()` o `PauseDownload()`, il metodo decrementa `qm.globalCount--`. Tuttavia, il `completeFn` (eseguito nel `defer` del worker `startDownload()`) decrementa **una seconda volta** `qm.globalCount--` quando il context viene cancellato.

Flusso:
1. `CancelDownload(id)` → `delete(qm.activeJobs, id)` + `qm.globalCount--` + `cancelFn()`
2. La goroutine worker riceve `ctx.Done()` → chiama `completeFn()` → `delete(qm.activeJobs, d.ID)` (no-op, già rimosso) + `qm.globalCount--` (SECONDA VOLTA!)

**Impatto:** `globalCount` diventa negativo. A quel punto `tryDispatch()` non rispetta più il limite `maxParallel` perché `activeCount` è negativo. Download illimitati partono in parallelo, saturando connessioni IRC e banda.

**Fix suggerito:** Il `completeFn` deve verificare se il download è ancora presente in `activeJobs` prima di decrementare. Alternativamente, usare un flag atomico `cancelled` per evitare il double-decrement.

---

### 2. `ircmanager.ConnectServer` — Race Condition su Connessioni Multiple

**File:** `internal/ircmanager/manager.go:205-265`  
**Categoria:** Race Condition

**Descrizione:** `ConnectServer()` controlla se esiste già una connessione tenendo il lock, ma poi **rilascia il lock** per fare cleanup (`existing.cancel()`, `time.Sleep(10ms)`) e ri-acquisisce il lock per inserire la nuova connessione. Nella finestra senza lock, un'altra goroutine può creare una seconda connessione per lo stesso serverID.

**Impatto:** Due connessioni IRC parallele sullo stesso server, con handler duplicati, JOIN doppi, possibile ban (K-Line) dal network IRC per flood.

**Fix suggerito:** Tenere il lock per tutta la durata dell'operazione, oppure usare un pattern di "prenotazione" con un flag `connecting` nella map.

---

### 3. `queue/events.go` — Data Race su `subscriberHub.subscribers`

**File:** `internal/queue/events.go:61-79`  
**Categoria:** Data Race

**Descrizione:** A differenza del `subscriberHub` in `ircmanager/manager.go` (che ha `sync.RWMutex`), il `subscriberHub` in `queue/events.go` **non ha alcun mutex**. I metodi `subscribe()`, `unsubscribe()`, e `publish()` accedono e modificano `h.subscribers` senza sincronizzazione.

**Impatto:** Se `Subscribe()` (da main.go) e `publish()` (da una goroutine worker) vengono chiamati concorrentemente, c'è una data race sullo slice. Può causare panic per slice bounds out of range o perdita di eventi.

**Fix suggerito:** Aggiungere `sync.RWMutex` come già fatto nell'ircmanager.

---

### 4. `handlers_sse.go` — Request ID Mai Recuperato (Usa Chiave Sbagliata)

**File:** `internal/api/handlers_sse.go:19-23` + `internal/api/api.go:175-183`  
**Categoria:** Logic Bug

**Descrizione:** Il middleware `RequestID` salva l'ID con una chiave tipizzata:
```go
const requestIDKey contextKey = "request-id"
ctx := context.WithValue(r.Context(), requestIDKey, id)
```
Ma l'handler SSE lo recupera con una stringa letterale:
```go
if id := r.Context().Value("request-id"); id != nil {
```
In Go, `context.WithValue` usa l'uguaglianza di interfaccia. Un `contextKey("request-id")` **non è uguale** alla stringa `"request-id"`. Il lookup fallisce sempre e il request ID sarà sempre `"unknown"`.

**Impatto:** Funzionalità di tracciamento request ID rotta per tutti gli handler che cercano di recuperarlo con la stringa. Log di debug inaccurati.

**Fix suggerito:** Usare `r.Context().Value(requestIDKey)` oppure esportare `requestIDKey` e usarlo in tutti gli handler.

---

## 🟠 HIGH

### 5. Channel Slots — Leak Permanente (Blocco Canale)

**File:** `internal/queue/manager.go:418-430`  
**Categoria:** Logic Bug / Resource Leak

**Descrizione:** `PauseDownload()` e `RemoveDownload()` chiamano `releaseChannelSlot(id)` solo se `d != nil && active`. Se `GetDownload(id)` fallisce (es. errore DB, record cancellato da un'altra goroutine), `releaseChannelSlot` **non viene chiamato** e lo slot `server|channel` rimane occupato in `qm.channelSlots` per sempre.

**Impatto:** Il canale IRC rimane bloccato — nessun altro download può partire su quel canale fino al riavvio del processo.

**Fix suggerito:** Chiamare `releaseChannelSlot(id)` incondizionatamente quando `active == true`, senza dipendere dal risultato di `GetDownload`.

---

### 6. `LogBroadcaster` — Buffer Overflow Silenzioso su Client Lenti

**File:** `internal/logging/broadcaster.go:60-65` (via `sse/hub.go:119-135`)  
**Categoria:** Design Smell / Data Loss

**Descrizione:** `sse.Hub.Publish()` usa `select { case ch <- evt: default: }` — se un client SSE ha il buffer pieno (connessione lenta, browser in background), gli eventi vengono **silenziosamente droppati**. Per i log questo è meno grave, ma per eventi di stato (download completati, server disconnessi) il frontend rimane con uno stato inconsistente.

**Impatto:** Il frontend può mostrare download "in corso" che in realtà sono già completati/falliti, o server "connessi" che sono disconnessi.

**Fix suggerito:** Quando il buffer di un client è pieno, chiudere la connessione SSE forzando il client a riconnettersi (sfruttando il meccanismo `Last-Event-ID` già implementato).

---

### 7. `logger.AddWriter` — Perde Writer Precedenti

**File:** `internal/logging/logging.go:115-128`  
**Categoria:** Logic Bug

**Descrizione:** `AddWriter()` ricostruisce il multi-writer da zero con `stderr + file + newWriter`. Se chiamato più di una volta, il writer aggiunto nella chiamata precedente **viene perso**.

```go
func (l *Logger) AddWriter(w io.Writer) {
    var writers []io.Writer
    writers = append(writers, os.Stderr)  // sempre aggiunto
    if l.file != nil { writers = append(writers, l.file) }
    writers = append(writers, w)  // solo l'ultimo
    l.logger.SetOutput(io.MultiWriter(writers...))
}
```

**Impatto:** Attualmente chiamato una volta sola (per `logBroadcaster`), quindi non è un bug attivo. Ma è fragile e sorprenderà un futuro manutentore.

**Fix suggerito:** Mantenere uno slice di writer aggiuntivi e includerli tutti nella ricostruzione.

---

### 8. Disconnessione Server — Timeout 30s Blocca Shutdown

**File:** `internal/ircmanager/manager.go:268-305`  
**Categoria:** Shutdown Issue

**Descrizione:** `DisconnectServer()` aspetta fino a **30 secondi** (10s + 20s) per la chiusura della goroutine `run()`. Durante lo shutdown, `Stop()` chiama `DisconnectServer` per ogni server in parallelo, ma se anche un solo server è lento, il `WaitGroup` blocca tutto.

**Impatto:** Se un server IRC è in fase di riconnessione con backoff di 80 secondi, `DisconnectServer` consumerà 30 secondi dello shutdown, potenzialmente eccedendo il timeout di sistema (es. systemd manda SIGKILL dopo 90s).

**Fix suggerito:** Ridurre il timeout complessivo a 5s o rendere il disconnect asincrono dopo un certo punto.

---

## 🟡 MEDIUM

### 9. Rotazione Log — Loop Infinito su Errore Rename

**File:** `internal/logging/logging.go:250-265`  
**Categoria:** Logic Bug

**Descrizione:** Se `os.Rename()` fallisce (es. permessi, file lock su Windows), il file non viene rinominato, un nuovo file viene aperto, ma il vecchio file rimane oltre `maxSizeMB`. Al prossimo log, la size è ancora sopra il limite, `rotateIfNeeded` viene chiamato di nuovo, il rename fallisce ancora, e così via.

**Impatto:** Loop infinito ad alta frequenza (ogni scrittura di log), consumo CPU 100%, file di log cresce senza limiti.

**Fix suggerito:** Se il rename fallisce, chiudere il file corrente e aprirne uno nuovo con un nome diverso (es. con timestamp), oppure disabilitare la rotazione per quel ciclo.

---

### 10. `ensureConnection` — Busy-Wait di 30 Secondi

**File:** `internal/ircmanager/manager.go:386-425`  
**Categoria:** Design Smell / Fragility

**Descrizione:** Il metodo usa un loop `for i := 0; i < 30; i++ { time.Sleep(1 * time.Second) }` per aspettare che una connessione venga stabilita. Non c'è alcun meccanismo di notifica (channel/cond), solo polling.

**Impatto:** Blocca il thread per fino a 30 secondi. Se chiamato da una richiesta HTTP, la richiesta va in timeout.

**Fix suggerito:** Usare un canale di notifica (già esiste `connectedCh` in `managedConnection`) o un `sync.Cond`.

---

### 11. `ConnectServer` — `time.Sleep(10ms)` Come Sincronizzazione

**File:** `internal/ircmanager/manager.go:214`  
**Categoria:** Fragility

**Descrizione:** Dopo aver cancellato il context di una vecchia connessione, il codice fa `time.Sleep(10 * time.Millisecond)` sperando che la goroutine abbia finito di pulire. Non c'è alcuna garanzia che 10ms siano sufficienti.

**Impatto:** Su sistemi sotto carico, 10ms potrebbero non bastare. La vecchia goroutine potrebbe ancora star eseguendo cleanup mentre la nuova connessione è già attiva, causando stati inconsistenti.

**Fix suggerito:** Usare `conn.wg.Wait()` come già fatto in `DisconnectServer()`.

---

### 12. `subscriberHub` Duplicato — Due Implementazioni Diverse

**File:** `internal/ircmanager/manager.go:1118-1141` e `internal/queue/events.go:61-79`  
**Categoria:** Code Duplication

**Descrizione:** Due implementazioni quasi identiche di `subscriberHub`:
- `ircmanager` ha `sync.RWMutex`
- `queue` NON ha sincronizzazione
- Buffer diversi (256 vs 512)
- Struttura leggermente diversa

**Impatto:** Manutenzione duplicata, bug fissati in una copia ma non nell'altra.

**Fix suggerito:** Estrarre in un package condiviso (es. `internal/pubsub`).

---

### 13. `sse.Hub.Publish` — Tiene il Lock Durante Tutto il Broadcast

**File:** `internal/sse/hub.go:119-135`  
**Categoria:** Design Smell

**Descrizione:** `Publish()` acquisisce `h.mu.Lock()` prima di iterare su tutti i client e scrivere nei loro canali. Se ci sono molti client (es. 100+) e alcuni sono lenti, il lock è tenuto per tempi significativi, bloccando tutte le altre operazioni sull'hub (nuove connessioni, `EventsSince`, ecc.).

**Impatto:** Degrado delle performance sotto carico. Nuovi client SSE potrebbero sperimentare latenza nella connessione.

**Fix suggerito:** Copiare la lista dei client sotto read-lock, poi iterare senza lock. Il pattern RLock + copia + RUnlock è standard.

---

### 14. DCC `receiveData` — Throttle Sleep Blocca Shutdown

**File:** `internal/irc/dcc.go:79-86`  
**Categoria:** Shutdown Issue

**Descrizione:** Quando il rate limiting è attivo, `receiveData()` chiama `time.Sleep(sleepTime)` senza check sul context. Se il download viene cancellato durante la sleep, la goroutine rimane bloccata fino alla fine della sleep.

**Impatto:** Ritardo nella cancellazione dei download (fino al tempo di sleep).

**Fix suggerito:** Usare `select { case <-ctx.Done(): return; case <-time.After(sleepTime): }`.

---

### 15. `managedConnection.run()` — Panic Recovery Incompleto

**File:** `internal/ircmanager/manager.go:668-700`  
**Categoria:** Robustness

**Descrizione:** Il `defer` in `run()` ha panic recovery, ma se il panic avviene in `mc.connect()` (che alloca canali, registra handler girc, spawna goroutine), il recovery non pulisce quelle risorse.

**Impatto:** In caso di panic, canali e goroutine potrebbero rimanere appesi (leak).

**Fix suggerito:** Nel recovery, chiudere esplicitamente l'IRC client e aspettare le goroutine spawnate da `connect()`.

---

## 🟢 LOW

### 16. `serverResponse.LastConnectedAt` — Potenziale Nil Pointer Dereference

**File:** `internal/api/handlers_server.go:56`  
**Categoria:** Potential Bug

**Descrizione:**
```go
if s.Status == "connected" && s.LastConnectedAt != nil {
    uptimeSeconds = int64(time.Since(*s.LastConnectedAt).Seconds())
}
```
Se lo status è `"connected"` ma `LastConnectedAt` è nil (possibile se il record DB ha un timestamp NULL), `time.Since(*s.LastConnectedAt)` causerebbe panic. Il check `!= nil` previene questo scenario, ma il check sullo status è ridondante — se `LastConnectedAt` è nil, non c'è uptime.

**Impatto:** Basso (il check `!= nil` protegge), ma il pattern è fragile.

---

### 17. `writeSSEEvent` — Ignora Errori di Serializzazione JSON

**File:** `internal/api/handlers_sse.go:133-143`  
**Categoria:** Error Handling

**Descrizione:** Se `json.Marshal(evt.Payload)` fallisce, la funzione ritorna silenziosamente senza scrivere l'evento SSE. Il client non riceve nulla e non sa che c'è stato un errore.

**Impatto:** Basso (payload sempre semplici map), ma in caso di bug il fallimento è silenzioso.

**Fix suggerito:** Loggare l'errore.

---

### 18. `irc.client.go` — `finishedCh` Channel Potenzialmente Chiuso Due Volte

**File:** `internal/irc/client.go:353-358`  
**Categoria:** Potential Panic

**Descrizione:** `finishSuccess()` usa `c.closeOnce.Do(func() { close(c.downloadDone) })`, che è corretto. Tuttavia il channel `downloadDone` viene ricreato in `resetForPack()` senza chiudere il precedente. Se `resetForPack()` viene chiamato mentre un'altra goroutine sta ancora leggendo dal vecchio `downloadDone`, potrebbe esserci una race.

**Analisi:** In pratica `resetForPack` viene chiamato solo tra un pack e l'altro, quando il download precedente è già finito, quindi non dovrebbe manifestarsi. Comunque il pattern non è robusto.

---

## 📋 Raccomandazioni Prioritarie

1. **Fix immediato:** `queue/events.go` → aggiungere `sync.RWMutex` al `subscriberHub` (5 minuti)
2. **Fix immediato:** `queue/manager.go` → prevenire il double-decrement di `globalCount` (15 minuti)
3. **Alta priorità:** `ircmanager/manager.go` → eliminare `time.Sleep(10ms)` in `ConnectServer`, usare `wg.Wait()` (30 minuti)
4. **Alta priorità:** `ircmanager/manager.go` → tenere il lock durante tutta `ConnectServer` per prevenire connessioni duplicate (20 minuti)
5. **Alta priorità:** `api/handlers_sse.go` → fixare il lookup del request ID con `requestIDKey` (5 minuti)
6. **Media priorità:** Estrarre `subscriberHub` in package condiviso (30 minuti)
7. **Media priorità:** `sse/hub.go` → non tenere il lock durante il broadcast (15 minuti)
8. **Bassa priorità:** Migliorare la gestione degli errori in `logging.go` per la rotazione log

---

## 📈 Metriche

| Metrica | Valore |
|---------|--------|
| File analizzati | 12 |
| Bug CRITICAL trovati | 4 |
| Bug HIGH trovati | 4 |
| Problemi MEDIUM trovati | 7 |
| Miglioramenti LOW suggeriti | 3 |
| Race condition identificate | 3 |
| Resource leak identificati | 2 |
| Problemi di shutdown | 3 |
| Duplicazione codice | 1 |

---

*Report generato da analisi automatica + revisione manuale del codice.*
