# Analisi Backend: Bug e Problemi di Concorrenza

Repository: `/mnt/extSSD/host/home/progetti/xdcc_server`  
Branch: `develop`  
Data analisi: 2026-06-15  
Scope: backend Go (`internal/*`, `cmd/xdcc-server/*`)

---

## 1. Riepilogo esecutivo

Il backend ha subito diversi interventi di correzione di concorrenza (vedi `docs/bugfixes/RACE_FIX_SUMMARY.md`). La maggior parte dei goroutine leaks e delle race condition classiche (map+mutex, WaitGroup, double close) sono stati corretti. Tuttavia restano **bachi di concorrenza reali e bug logici** che possono causare: doppio dispatch di download, inconsistenze di stato, file descriptor lasciati aperti, panici per closed channel e perdita di slot di canale. Questo documento elenca i problemi rilevati e le fix applicabili, con riferimenti di riga.

---

## 2. Bug di concorrenza e di stato

### 2.1 `queue.Manager`: `tryDispatch` è protetto da `dispatchMu`, ma `startDownload` non acquisisce il lock sulle strutture comuni sotto `dispatchMu`

**File:** `internal/queue/manager.go:527-621`, `internal/queue/manager.go:623-864`

**Problema:** `tryDispatch` acquisisce `dispatchMu` e controlla `globalCount`/`channelSlots`. Subito dopo rilascia `dispatchMu` e chiama `startDownload`, che in un'altra goroutine aggiorna `globalCount`/`activeJobs`/`channelSlots` sotto `qm.mu`. Tra il check e la scrittura di `startDownload` può intercorrere tempo (DB + start goroutine). Un secondo `tryDispatch` concorrente (da Enqueue, monitorLoop, disk callback) può vedere `activeCount < maxParallel` e lo stesso slot libero, e lanciare un secondo download per lo stesso canale.

**Impatto:** Due download per lo stesso bot/canale possono partire in parallelo, violando il vincolo "1 active per channel".

**Fix consigliata:** Pre-acquisire lo slot **prima** di rilasciare `dispatchMu`. In `tryDispatch`, dopo il controllo che il canale è libero, inserire l'id di prenotazione in `channelSlots` e incrementare `globalCount` atomiciamente sotto `qm.mu` (o farlo in `startDownload` ma con un flag che torni indietro se fallisce). `startDownload` non deve fallire dopo il rilascio del lock senza rimuovere il placeholder. Alternativa: spostare l'intero `startDownload` (inclusa `MarkDownloadStarted` e la prenotazione dello slot) sotto `dispatchMu` oppure rendere atomica la prenotazione con una riga che `startDownload` trasforma in job attivo.

---

### 2.2 `queue.Manager`: rilascio slot non atomico con `activeJobs`/`globalCount`

**File:** `internal/queue/manager.go:737-753`, `internal/queue/manager.go:866-880`

**Problema:** `completeFn`, `PauseDownload`, `RemoveDownload`, `cancelDownloadWithCtx` cancellano il job da `activeJobs` e decrementano `globalCount` sotto `qm.mu`, ma rilasciano lo slot in `releaseChannelSlot` con un secondo lock. È possibile che tra i due lock un altro `tryDispatch` legga `channelSlots` ancora occupato e salti il canale, mentre `globalCount` è già decrementato, sprecando un slot globale. Viceversa, se un errore o un panic colpisce tra il primo e il secondo lock, lo slot può restare bloccato per sempre.

**Impatto:** Inconsistenza conteggio/slot; in caso peggiore slot persistente fino al riavvio.

**Fix consigliata:** Rilasciare slot e decrementare conteggio nello stesso blocco critico sotto `qm.mu`. Rimuovere la funzione `releaseChannelSlot` come operazione separata e passare la chiave `slotKey` all'interno dello stesso lock in modo da non perdere coerenza.

---

### 2.3 `queue.Manager`: `SetIRCManager` acquisisce `qm.mu` ma non impedisce corse con `tryDispatch` in lettura di `qm.ircMgr`

**File:** `internal/queue/manager.go:147-153`, `internal/queue/manager.go:683-684`

**Problema:** `SetIRCManager` è pensato per essere chiamato prima di `Start()`, ma il contratto non è forzato. Se chiamato durante il funzionamento, `tryDispatch` legge `qm.ircMgr` senza lock mentre `SetIRCManager` scrive sotto `qm.mu`. Non c'è data race strutturale sul pointer (Go atomicizza load di pointer), ma il nuovo manager può essere usato a metà inizializzazione oppure due download consecutivi usano due manager diversi.

**Impatto:** Comportamento non deterministico; potenzialmente download con manager non pronto.

**Fix consigliata:** Rendere `SetIRCManager` impossibile dopo `Start()` (panic o errore), oppure usare `atomic.Pointer` per `ircMgr`. Documentare e forzare il setup pre-Start.

---

### 2.4 `queue.Manager`: `diskLow` è aggiornato sotto `qm.mu`, ma il callback del disk monitor può invocare `tryDispatch` durante `Stop()`

**File:** `internal/queue/manager.go:124-140`, `internal/queue/manager.go:185-239`

**Problema:** In `Stop()` vengono fermati il timer, il disk monitor, poi `cancel()`, poi `subscriber.Close()`, e infine `cancelDownloadWithCtx`. Se durante `Stop()` il callback del disk monitor (eseguito in un'altra goroutine) chiama `tryDispatch()` dopo che `cancel()` è stato chiamato, `tryDispatch` ritorna per `ctx.Done()`; ma se chiama prima di `cancel()`, può partire un nuovo download mentre `Stop()` sta cancellando i job attivi. Inoltre `startDownload` può partire mentre `subscriber.Close()` è già stato chiamato: l'evento `EventDownloadStarted` andrà su hub chiuso e verrà scartato, ma il download continua a consumare risorse.

**Impatto:** Race tra shutdown e avvio download; eventi persi; possibile goroutine fantasma durante stop.

**Fix consigliata:** Introduzione di un flag `closing` atomico. `Stop()` lo setta **prima** di fermare il disk monitor e il timer. `tryDispatch` e `Enqueue` devono controllarlo e rifiutare ogni nuovo lavoro. Solo dopo che `closing` è true e nessun `tryDispatch` è in corso si può procedere con `cancel()` e la chiusura.

---

### 2.5 `queue.Manager`: `metadataEmitted` e `startTime` sono variabili della goroutine ma non thread-safe per l'accesso esterno

**File:** `internal/queue/manager.go:666-673`, `internal/queue/manager.go:696-735`

**Problema:** `metadataEmitted` e `startTime` sono catturate nella chiusura del progress callback. Il progress callback viene chiamato dalla goroutine del download worker, quindi non c'è race su di esse. **Tuttavia** `metadataEmitted` influenza la chiamata a `store.UpdateDownloadMetadata` e `emitEvent` ma non è protetto contro doppia emissione se il progress callback viene eseguito contemporaneamente da due goroutine (nel codice attuale non succede, ma è un fragilità). Inoltre `startTime` può essere letto in `completeFn` (stessa goroutine) — ok per ora, ma entrambe sono variabili condivise tra due callback invocati dalla stessa goroutine; nessun problema reale, ma **mancanza di documentazione/commento** che ne chiarisca la sicurezza.

**Impatto:** Basso, ma rende il codice fragile a future modifiche.

**Fix consigliata:** Documentare nel commento che entrambe le variabili sono accessibili solo dal worker goroutine. Non serve lock, ma evitare future refactoring che le leggano dall'esterno. Non c'è azione immediata se non aggiungere un commento.

---

### 2.6 `queue.Manager`: `startDownload` usa `qm.ctx` per chiamate DB dopo che il context potrebbe essere annullato

**File:** `internal/queue/manager.go:626`, `internal/queue/manager.go:711`, `internal/queue/manager.go:727`, ecc.

**Problema:** `startDownload` usa `qm.ctx` per `MarkDownloadStarted`, `UpdateDownloadProgress`, `UpdateDownloadMetadata`, `MarkDownloadCompleted`, etc. Quando `qm.ctx` viene cancellato in `Stop()`, le callback di progress e complete del download in corso eseguono query che falliscono immediatamente con `context.Canceled`, anche se `cancelDownloadWithCtx` passa un `shutdownCtx` con timeout. Questo può lasciare lo stato del download non aggiornato.

**Impatto:** Dati incoerenti in shutdown: progresso finale perso, stato non aggiornato a failed/paused.

**Fix consigliata:** Nei callback del download worker, usare `context.Background()` o un context derivato da `shutdownCtx` per le operazioni di persistenza finali, oppure passare un context non legato a `qm.ctx` durante lo shutdown. La soluzione più semplice è: in `completeFn` e `progressFn`, quando `ctx` del download è `Done()`, usare `context.Background()` per gli ultimi store update. In `Stop()` assicurarsi che tutti i callback completino prima di chiudere il DB.

---

### 2.7 `ircmanager.Manager`: `ConnectServer` è vulnerabile a riconnessioni duplicate per lo stesso server dopo `ConnectServer` concluso con errore

**File:** `internal/ircmanager/manager.go:262-342`

**Problema:** `ConnectServer` controlla `m.conns[srv.ID]` e `m.connecting`. Ma se un `ConnectServer` fallisce (es. `store.GetChannelsByServer` restituisce errore o l'errore non resetta lo stato), il `defer` che cancella `m.connecting` gira. Tuttavia `m.conns[srv.ID]` potrebbe essere stato già sovrascritto con una nuova `managedConnection` (riga 327) anche se `connect()` non è ancora partito. Una seconda goroutine può vedere la connessione come "not connected" e chiamare di nuovo `ConnectServer`, creando un'altra connessione.

**Impatto:** Doppie connessioni verso lo stesso server, conflitto di nick, doppio JOIN, race sul DB.

**Fix consigliata:** Inserire `m.conns[srv.ID] = conn` solo **dopo** che il setup è completato con successo e subito prima di avviare `go conn.run()`. Se la fase precedente ( caricamento canali, setup DB) fallisce, non modificare `m.conns`, così il prossimo `ConnectServer` vede ancora lo stato precedente. Inoltre lo stato "connecting" va scritto in `m.conns` con una connessione placeholder se si vuole evitare duplicati, ma la placeholder deve essere coerente.

---

### 2.8 `ircmanager.Manager`: `ConnectServer` e `DisconnectServer` in parallelo possono perdere il riferimento a `oldConn`

**File:** `internal/ircmanager/manager.go:288-302`, `internal/ircmanager/manager.go:344-399`

**Problema:** `ConnectServer` prende `oldConn` sotto lock, poi rilascia il lock e chiama `oldConn.cancel()` + `oldConn.wg.Wait()`. Durante l'attesa, `DisconnectServer` può rimuovere `m.conns[srv.ID]` e aspettarsi di cancellare `oldConn`. Quando `ConnectServer` riacquisisce il lock e sovrascrive `m.conns`, perde il riferimento gestito da `DisconnectServer`, e le due goroutine aspettano sulla stessa WaitGroup, causando potenzialmente un timeout per entrambe.

**Impatto:** Timeout di shutdown, stato DB errato, possibile goroutine leak.

**Fix consigliata:** Rendere `ConnectServer` atomico rispetto a `DisconnectServer`: dopo aver deciso di riconnettere, mantenere il lock o un flag per-server fino a quando `oldConn` non è completamente terminato e `m.conns` è aggiornato. Evitare di rilasciare il lock tra la decisione e l'inserimento.

---

### 2.9 `managedConnection.run`: `connectedCh` può essere chiuso due volte se `connect()` ritorna prima di CONNECTED e `disconnect()` annulla il context

**File:** `internal/ircmanager/manager.go:911-939`

**Problema:** `connect()` dichiara `var closeConnectedOnce sync.Once` e un `defer` che chiude `connectedCh`. Il CONNECTED handler chiude la stessa `connectedCh`. Il `defer` chiude sempre al ritorno. Se CONNECTED è arrivato e `connect()` esce normalmente (Phase 2), `closeConnectedOnce.Do` è già eseguito, il `defer` non fa nulla. Se però `connect()` ritorna in Phase 1 **dopo** che `client.Handlers` ha già chiuso `connectedCh` (caso strano, es. handler errato), `sync.Once` protegge. Quindi il meccanismo è corretto. Il problema vero è che `waitConnected()` (riga 1220) legge `mc.connectedCh` e attende la chiusura, ma subito dopo il ritorno legge `mc.Status()`; se tra la chiusura e la lettura di `Status()` la connessione cade o `disconnect()` cambia lo stato, `waitConnected` ritorna `false` anche se era connesso. Questo è un **TOCTOU** tra `connectedCh` e `status`.

**Impatto:** `ensureConnection` può fallire o tornare una connessione "connected" che in realtà non lo è più, portando a download che falliscono subito.

**Fix consigliata:** In `waitConnected`, dopo la chiusura di `ch`, rileggere `mc.connectedCh` e, se diverso, riprovare. Oppure restituire il `*girc.Client` insieme allo stato per verificare la connessione live. La soluzione più robusta è: la connessione è considerata "pronta" solo quando `mc.Status() == "connected"` e `mc.irc != nil`, non solo quando il canale è chiuso. Verificare atomico lo stato sotto `mc.mu` e non usare il canale come unico segnale.

---

### 2.10 `managedConnection.run`: panic recovery e `mc.irc.Close()` senza nil-check atomico

**File:** `internal/ircmanager/manager.go:823-844`

**Problema:** Il `defer` di recovery legge `mc.irc` sotto `mc.mu.RLock()` e chiama `Close()` se non nil. Se tra il check e la chiamata un altro goroutine ha già chiamato `Close()`, la seconda `Close()` potrebbe essere safe (dipende da girc), ma non è garantito. Inoltre `mc.irc` viene assegnato nel CONNECTED handler sotto `mc.mu.Lock()`; la lettura sotto RLock è corretta. Il problema maggiore è che `recover()` cattura il panic, ma poi chiama `mc.manager.store.SetServerStatus(context.Background(), ...)` con un context non legato a `mc.ctx`; se lo store è chiuso in shutdown, fallisce silenziosamente. Non è un bug di concorrenza ma una fragilità di lifecycle.

**Impatto:** Panic silenziato, stato potenzialmente non aggiornato, doppia chiusura client.

**Fix consigliata:** Aggiungere nil-check e un flag `ircClosed` atomico per evitare doppia Close. In `Close` e `connect` cleanup, impostare `mc.irc = nil` dopo Close. Non è critico ma raccomandato per robustezza.

---

### 2.11 `managedConnection.disconnect`: non attende `run()` e non pulisce `m.conns`

**File:** `internal/ircmanager/manager.go:1202-1208`, `internal/ircmanager/manager.go:344-399`

**Problema:** `disconnect()` setta solo lo stato e chiama `cancel()`. Non rimuove la connessione da `m.conns` né attende `wg.Wait()`. La rimozione da `m.conns` è responsabilità di `DisconnectServer`. Se `run()` è in fase di backoff e `cancel()` è chiamato, `run()` esce pulendo il DB, ma la struttura in `m.conns` resta fino a che `DisconnectServer` non la toglie. In `ConnectServer`, `oldConn.cancel()` + `oldConn.wg.Wait()` è chiamato, ma se `oldConn` è in backoff, `wg.Wait()` attende il ritorno dal backoff, che è corretto. Tuttavia `DisconnectServer` non chiama `cancel()` prima di `wg.Wait()`? Sì, chiama `conn.disconnect()` (riga 368). Ma il timeout di 15s in `DisconnectServer` può scadere mentre `run()` è bloccato in `reconnectBackoff` su una sleep di 1h. In tal caso `DisconnectServer` restituisce errore, ma il goroutine continua a vivere perché `cancel()` è stato chiamato e `mc.ctx` è cancellato, quindi la select su `mc.ctx.Done()` in `reconnectBackoff` dovrebbe uscire. Quindi è corretto. Il problema è il timeout di 15s potrebbe non bastare se il backoff è appena iniziato (5s, 10s, 20s, 40s, 80s, 1h). In pratica 15s copre solo i primi due backoff. Se il backoff è 20s o più, `DisconnectServer` dà timeout anche se poi il goroutine muore quasi subito (entro 20s). Questo è un **falso positivo di timeout** che logga errore e ritorna errore al chiamante, ma il goroutine non è un leak.

**Impatto:** Utente/API vede errore di shutdown anche se il goroutine sta per uscire. Stress test e shutdown pulito possono fallire.

**Fix consigliata:** Non ritornare errore se il goroutine esce dopo il timeout; oppure usare un segnale di "done" già esistente e considerare il timeout solo come attesa massima, poi verificare `!conn.IsRunning()` per decidere se è leak. Riformulare il messaggio: "shutdown exceeded timeout but goroutine may still exit; check IsRunning".

---

### 2.12 `managedConnection.sendChannelGreeting`: non verifica se il client è ancora valido dopo il timer

**File:** `internal/ircmanager/manager.go:1262-1300`

**Problema:** La goroutine del greeting aspetta il timer e il context, poi invia `mc.sendChannelMsg(cl, channel, greeting)`. Il `cl` è catturato nello scope. Se il client è stato riconnesso (nuovo girc.Client) o la connessione è caduta, `cl` punta a un client chiuso. La chiamata a `cl.Cmd.Message` potrebbe fallire o inviare su una connessione chiusa. Non c'è crash, ma log di errore e potenziale invio su connessione errata.

**Impatto:** Messaggi inviati su client scaduto, errori di log spuri.

**Fix consigliata:** Prima di inviare, rileggere `mc.irc` sotto `mc.mu.RLock()` e usarlo solo se corrisponde a `cl`. Oppure catturare `mc` e usare `mc.irc` corrente. In generale non catturare puntatori girc.Client in goroutine long-lived senza verificarne la validità.

---

### 2.13 `channellog.Logger`: `getOrOpen` apre il file sotto `l.mu`, ma non c'è limite al numero di file aperti

**File:** `internal/channellog/channellog.go:309-326`

**Problema:** Ogni canale distinto crea un file descriptor. Se un attaccante o un bug invia log per migliaia di canali diversi, il processo può esaurire i file descriptor. Inoltre i file restano aperti per la vita del Logger. Il `syncLoop` itera su tutti i file ogni 30s; con molti file il lock `l.mu` è tenuto per tempo proporzionale al numero di file aperti, bloccando le chiamate `Log`/`LogPrivate`.

**Impatto:** Esaurimento file descriptor, latenza sul log, possibile DoS locale.

**Fix consigliata:** Limitare il numero di file aperti (es. LRU con cap, o chiudere file inattivi dopo inattività). Dato che il log è opt-in e nascosto, un cap di 100 canali è ragionevole. Inoltre il `syncLoop` dovrebbe acquisire `l.mu` solo per copiare la slice, poi rilasciarlo subito (già così), ma se la copia è molto lunga (migliaia di file) il lock è comunque tenuto per la copia. Accettabile con cap.

---

### 2.14 `channellog.Logger`: `Close` non è idempotente in modo sicuro se chiamata più volte in parallelo

**File:** `internal/channellog/channellog.go:278-307`

**Problema:** `Close` usa `select` con `close(l.stopCh)` protetto da `default`. Se due goroutine chiamano `Close` contemporaneamente, entrambe possono passare il `default` e una chiudere `stopCh` mentre l'altra è nel `select`, causando panic per chiudere un canale già chiuso. La select protegge solo se il canale è già chiuso all'ingresso, non durante la chiamata a `close`.

**Impatto:** Panico `close of closed channel` nel processo.

**Fix consigliata:** Proteggere `Close` con `sync.Once` o un flag `closed` atomico. Esempio:

```go
var closeOnce sync.Once
func (l *Logger) Close() error {
    closeOnce.Do(func() { close(l.stopCh) })
    ...
}
```

Ma `closeOnce` deve essere un campo di `Logger` per permettere idempotenza tra istanze. In alternativa, usare `atomic.Bool` e `close` sotto `l.mu`.

---

### 2.15 `channellog.Logger`: `Log` e `LogPrivate` possono riaprire un file chiuso in modo concorrente con `Close`

**File:** `internal/channellog/channellog.go:171-216`, `internal/channellog/channellog.go:228-274`, `internal/channellog/channellog.go:278-307`

**Problema:** `Close` cancella `l.files` e chiude i file. Se una chiamata `Log` è in corso e ha già ottenuto `cf` da `getOrOpen`, ma non ha ancora acquisito `cf.mu`, `Close` può acquisire `cf.mu`, chiudere e azzerare `cf.f`, e anche rimuovere `cf` da `l.files`. Poi `Log` acquisisce `cf.mu`, vede `cf.f == nil`, riapre il file (riga 186-193) e scrive. Questo è intenzionale ma produce un file riaperto dopo la `Close` e non più sincronizzato dal `syncLoop`. Non è un crash, ma il log continua a scrivere dopo la chiusura.

**Impatto:** File di log scritti dopo `Close`, rischio di fd leak se il Logger è chiuso e poi getOrOpen riapre.

**Fix consigliata:** Aggiungere un flag `closed` a `Logger` e far ritornare `Log`/`LogPrivate` silenziosamente se chiuso. In `Close`, impostare `closed` prima di iterare i file, e far fallire `getOrOpen` se chiuso. Inoltre, dopo aver rimosso `cf` da `l.files`, chiudere `cf.f` e impostare un flag `closed` su `channelFile` così `Log` non lo riapre.

---

### 2.18 `irc.Client`: `packIdxVal` è atomic, ma `currentPack()` non ha bounds check e `c.packs` può essere vuoto

**File:** `internal/irc/client.go:457-459`

**Problema:** `currentPack()` carica `c.packs[c.packIdxVal.Load()]`. Se `c.packs` è vuoto, panico. `NewClient` richiede che tutti i pack siano sullo stesso server, ma non controlla `len(packs) > 0`. `DownloadAll` usa `c.packs[0]` senza check (righe 214, 219, 230). Un caller errato (es. watchlist che passa slice vuota) causa panic.

**Impatto:** Panico del processo.

**Fix consigliata:** In `NewClient`, restituire errore se `len(packs) == 0`. In `DownloadAll`, controllare `len(c.packs) == 0` e restituire slice vuota/errore. In `currentPack`, aggiungere bounds check e ritornare `nil` con gestione negli handler.

---

### 2.19 `irc.Client`: `registerHandlers` cattura `c.currentPack()` in closure a runtime; se il pack cambia durante l'esecuzione del handler può usare dati sbagliati

**File:** `internal/irc/handlers.go:31-275`

**Problema:** Gli handler girc sono registrati una volta e usano `c.currentPack()` per leggere `Bot`, `Channel`, ecc. `currentPack()` legge `c.packIdxVal` atomicamente. Se il download del pack corrente finisce mentre un handler è in esecuzione (es. NOTICE ritardato), `packIdxVal` può essere già incrementato e `currentPack()` ritorna il pack successivo. Questo è pericoloso negli handler di `ERR_NOSUCHNICK` e `NOTICE`, che confrontano il nick del bot con il pack corrente. Se il pack è già cambiato, un NOTICE per il pack precedente può essere attribuito al pack successivo, causando errori o complete errati.

**Impatto:** Correlazione errata di eventi IRC a pack; download successivi possono essere completati/falliti per cause del pack precedente.

**Fix consigliata:** Catturare il pack corrente all'ingresso dell'handler e passarlo alla funzione di gestione, invece di chiamare `currentPack()` ripetutamente. Per NOTICE, confrontare il mittente con `pack.Bot` catturato. 

---

### 2.20 `irc.Client`: `whoisFallbackTimer` è un timer condiviso su `Connection` ma non protetto da mutex

**File:** `internal/irc/handlers.go:46-69`, `internal/irc/handlers.go:154-175`, `internal/irc/client.go:465-475`

**Problema:** `whoisFallbackTimer` è un campo di `Connection`. Gli handler girc sono eseguiti dalla goroutine di girc, mentre `downloadPackAtIndex` e `resetForPack` sono eseguiti dalla goroutine principale del download. In `RPL_ENDOFWHOIS`, `JOIN` e `resetForPack` si chiamano `stopWhoisFallbackTimer()`, `Reset()`, e si legge/scrive `c.conn.whoisFallbackTimer`. Non c'è mutex che protegga il timer. Anche se `time.Timer` è thread-safe per `Stop`/`Reset` in Go 1.23+ (documentazione ufficiale), il codice chiama `Reset` dopo `Stop` senza drenare sempre correttamente, e il `select` in `RPL_ENDOFWHOIS` (riga 57) usa `c.conn.whoisFallbackTimer.C` che può essere un canale vecchio se un reset concorrente lo ha sostituito. In Go 1.23+ `Timer.Reset` non alloca un nuovo canale, ma il problema resta concettuale.

**Impatto:** Race condition sul timer; potenziale invio di XDCC con ritardo errato o doppio invio.

**Fix consigliata:** Sincronizzare l'accesso al timer con un mutex dedicato. La soluzione più semplice è una `sync.Mutex` in `Connection` per proteggere il timer e il flag `messageSent`.

---

### 2.21 `irc.Client`: `messageSent`, `whoisFoundChannels`, `needsJoin` sono `atomic.Bool`, ma gli handler fanno decisioni basate su più flag senza snapshot atomico

**File:** `internal/irc/handlers.go:31-175`

**Problema:** In `RPL_ENDOFWHOIS`, il codice controlla `c.ps.messageSent.Load()`, `c.ps.needsJoin.Load()`, `c.ps.whoisFoundChannels.Load()` in sequenza. Se un altro handler cambia uno di questi flag tra le letture, la logica decisionale può essere inconsistente. Esempio: `needsJoin` è true, `messageSent` diventa true in un altro handler (JOIN), poi `RPL_ENDOFWHOIS` legge `messageSent=false` (stale? no, atomic) e decide di inviare XDCC. In realtà `messageSent.Swap(true)` in `sendXDCCRequest` è atomico, quindi non c'è doppio invio. Ma il check multi-flag non è atomico come gruppo, quindi il branching può essere inconsistente (es. invio XDCC anche se `needsJoin` è diventato false nel frattempo). Non è grave ma è un punto di fragilità.

**Impatto:** Decisioni logiche inconsistenti, possibile invio XDCC in condizioni non ottimali.

**Fix consigliata:** Raccogliere uno snapshot dei flag in una piccola struttura sotto `c.ps.mu` (o un nuovo mutex) quando si prendono decisioni.

---

### 2.24 `api/api.go`: `MsgRateLimiter` è `atomic.Pointer[RateLimiter]`, ma non c'è meccanismo di pulizia per IP inattivi

**File:** `internal/api/api.go:47`, `internal/api/ratelimit.go`

**Problema:** `RateLimiter` tiene una mappa di IP → timestamp. Se il server gira a lungo, la mappa cresce indefinitamente. Non c'è eviction o TTL. Questo è un memory leak lento.

**Impatto:** Crescita indefinita della memoria; potenziale DoS per riempimento della mappa.

**Fix consigliata:** Nel metodo `Allow` scartare le entry obsolete durante l'operazione.

---

### 2.26 `config/config.go`: `Load` e `Save`/`SaveRaw`/`ApplyPartial` non sono thread-safe

**File:** `internal/config/config.go:357-384`, `internal/config/config.go:907-1134`

**Problema:** `Config` è un puntatore condiviso tra `api`, `ircmanager`, `queue`, etc. `Load` ritorna una nuova istanza, ma `Save`/`ApplyPartial` modificano l'istanza corrente. Se un handler chiama `Save` mentre `ircmanager` legge `cfg.IsChannelBlacklisted` o `cfg.IRC.ChannelLog`, c'è una data race. Go race detector rileverebbe letture e scritture simultanee sui campi della struct. Il codice attuale non usa mutex su `Config`.

**Impatto:** Data race in runtime, stato inconsistente, possibile crash in race detector.

**Fix consigliata:** Rendere `Config` immutabile dopo il caricamento: ogni modifica produce una nuova istanza che viene sostituita con `atomic.Pointer` o `sync.RWMutex`. I consumer leggono una snapshot. Per campi che cambiano spesso (es. `ChannelLog`, `Greetings`), è possibile usare `atomic.Pointer` o copia. Soluzione pratica: usare `sync.RWMutex` in `Config` per proteggere le letture e scritture, oppure passare sempre una copia deep ai consumer. La soluzione più semplice e robusta: `API` e `Manager` tengono un `atomic.Pointer[*Config]` e lo scambiano quando la config viene ricaricata. In alternativa, rendere `Config` a prova di race con RWMutex e metodi di lettura.

---

### 2.27 `config/config.go`: `PickGreeting` e `IsChannelBlacklisted`/`IsChannelLogged` non sono thread-safe rispetto a scritture su `Greetings`/`ChannelLog`

**File:** `internal/config/config.go:843-896`

**Problema:** `ChannelLog` e `Greetings` sono slice. La lettura (`len`, iterazione) e la scrittura (`append` in ApplyPartial/SaveRaw) non sono atomiche. Se `ApplyPartial` modifica la slice mentre `ircmanager` itera su `ChannelLog` nel handler di log, c'è data race. `len(slice)` è una lettura sotto il cofano; in Go non è atomicamente sicura rispetto a scritture concorrenti.

**Impatto:** Data race, panic di runtime (slice bounds out of range o lettura di dati corrotti).

**Fix consigliata:** Proteggere con `sync.RWMutex` o usare slice immutabili (copia su scrittura). In `IsChannelLogged`, `IsChannelBlacklisted`, `PickGreeting` acquisire RLock. Nelle funzioni di scrittura acquisire Lock e sostituire la slice.

---

### 2.28 `config/config.go`: `SaveRaw` e `ApplyPartial` fanno backup e scrittura su file condiviso, ma non c'è lock di file

**File:** `internal/config/config.go:907-1134`

**Problema:** Due richieste HTTP possono chiamare `SaveRaw`/`ApplyPartial` contemporaneamente. Entrambe fanno `os.Stat`, `copyFile`, `os.WriteFile(tmp)`, `os.Rename`. Se interleave, il file `.bak` può essere sovrascritto, il `.tmp` può essere scambiato, o il `Rename` sovrascrive il file mentre un altro processo lo legge. In Go `os.Rename` è atomico su Unix, ma non tra le operazioni di backup. Il risultato è un file YAML potenzialmente inconsistente o backup perso.

**Impatto:** Corruzione o perdita della configurazione in caso di update concorrenti.

**Fix consigliata:** Aggiungere un `sync.Mutex` in `Config` per serializzare scritture su file. Le letture possono procedere in parallelo; le scritture no. Inoltre, l'API dovrebbe rifiutare update concorrenti o serializzarli a livello di handler.

---

### 2.29 `ircmanager.Manager`: `GetServers` e `GetChannels` leggono `m.conns` con RLock, ma il campo `Status` di `ServerRecord` in memoria è una copia; la modifica dello store da un'altra goroutine non è rilevante, ma l'overlay di stato è concorrente

**File:** `internal/ircmanager/manager.go:443-494`

**Problema:** `GetServers` chiama `m.store.ListServers` (operazione potenzialmente lenta) tenendo `m.mu.RLock()`. Questo blocca scrittori su `m.conns` mentre il DB viene interrogato. Se la lista è lunga o il DB è lento, il lock è tenuto per tempo. Inoltre `GetChannels` chiama `m.store.GetChannelsByServer` senza tenere `m.mu` durante la query, poi acquisisce RLock per leggere `m.conns`. Questo è migliore, ma l'overlay di stato `conn.joinedChs` e `conn.Status()` è fatto sotto RLock, ok.

**Impatto:** Blocco di `ConnectServer`/`DisconnectServer` durante le query di lettura; latenza sulle operazioni di gestione connessioni.

**Fix consigliata:** In `GetServers`, rilasciare `m.mu` prima di chiamare `ListServers`, poi riacquisire solo per l'overlay. In `GetChannels` è già fatto correttamente; verificare che non ci sia altra logica che tenga il lock inutilmente.

---

### 2.30 `queue.Manager`: `BulkAction` non protegge la mappa `results` con lock, ma è locale al chiamante quindi ok; però emette un singolo evento SSE senza dettagli

**File:** `internal/queue/manager.go:473-501`

**Problema:** `BulkAction` itera sugli ID e chiama metodi che acquisiscono `qm.mu`. La mappa `results` è locale e non condivisa, quindi nessuna race. L'evento `EventDownloadBulkResult` è emesso una sola volta alla fine, senza dettagli su quali ID hanno successo/fallito. Questo non è un bug di concorrenza ma un problema di UX/SSE.

**Impatto:** Frontend non riceve dettagli dell'azione bulk.

**Fix consigliata:** Includere `results` nel payload dell'evento SSE, oppure emettere un evento per ogni ID.

---

### 2.31 `managedConnection`: `isRunning` e `wg` sono controllati da due lock diversi; relazione non sempre chiara

**File:** `internal/ircmanager/manager.go:745-789`, `internal/ircmanager/manager.go:809-820`

**Problema:** `runningMu` protegge `isRunning`, `mu` protegge `status`, `wg` è una WaitGroup. `run()` controlla `isRunning` sotto `runningMu`, lo setta a true, poi fa `defer wg.Done()`. `ConnectServer` aspetta `oldConn.wg.Wait()` senza controllare `isRunning`. Se `oldConn` è stato creato ma `run()` non è mai partito (per esempio per via di una condizione di gara), `wg.Wait()` potrebbe aspettare un contatore che non è mai stato incrementato. Tuttavia `ConnectServer` incrementa `wg` solo quando avvia `go conn.run()`, quindi se `run()` non è partito, `wg` non è incrementata. In realtà `wg.Add(1)` è fatto in `ConnectServer` (riga 338) prima di `go conn.run()`. Se `run()` esce subito per `isRunning` già true (non dovrebbe), `wg.Done()` decrementa. Se `run()` non è mai chiamato per un errore prima di `go`, `wg.Wait()` resta bloccata. Questo scenario è possibile: se `ConnectServer` fallisce tra `wg.Add(1)` e `go conn.run()` (es. panic nel setup), `wg.Wait()` si blocca per sempre.

**Impatto:** Deadlock in `ConnectServer` o `DisconnectServer` in caso di panic nel setup della connessione.

**Fix consigliata:** Assicurarsi che `wg.Done()` sia chiamato in un `defer` in `ConnectServer` stesso o in un wrapper, in modo che anche se `go conn.run()` non viene lanciato il contatore venga decrementato. Oppure, spostare `wg.Add(1)` dentro `run()` all'inizio, ma `ConnectServer` deve aspettare un segnale che `run()` è partito. La soluzione più semplice è usare un `sync.Cond` o un canale `startedCh` per segnalare che `run()` è iniziato, e `wg` viene incrementata solo in `run()`.

---

### 2.32 `queue.Manager`: `tryDispatch` e `startDownload` condividono `pack` modificato dal worker; `completeFn` legge `pack.Filename` senza lock

**File:** `internal/queue/manager.go:656-665`, `internal/queue/manager.go:724-734`, `internal/queue/manager.go:815-822`

**Problema:** `pack` è un `*entities.XDCCPack` condiviso tra il worker di download e il manager di coda. Il worker aggiorna `pack.Filename`, `pack.Size`, `pack.Channel` tramite i handler IRC. `completeFn` e il progress callback nel manager leggono `pack.Filename` e `pack.Size` senza mutex. Sebbene in Go la lettura di un pointer su architetture a 64 bit sia atomica, il codice legge stringhe e int64 che sono più grandi di una word e non atomiche. Inoltre `pack.Channel` è una stringa. La lettura concorrente di `pack.Filename` mentre viene scritta può produrre stringa corrotta o panic (raro, ma possibile in race detector).

**Impatto:** Data race su `pack` fields; letture di stringhe in fase di scrittura.

**Fix consigliata:** Sincronizzare l'accesso a `pack` con un mutex, oppure usare solo valori restituiti dal worker tramite `workerResult` (che già contiene `Filename` e `FileSize`). Non leggere `pack.Filename`/`pack.Size` direttamente dal manager; usare `result.Filename`/`result.FileSize`. In `completeFn` usare `result.Filename` invece di `pack.Filename`. Questo elimina la condivisione di stato mutabile.

---

### 2.33 `irc.Client`: `DownloadAll` chiude `c.conn.irc` multiple volte se ci sono errori fatali

**File:** `internal/irc/client.go:232-295`

**Problema:** `closeConn` è chiamato nel loop in caso di errore fatale, e poi di nuovo alla fine. `c.conn.irc.Close()` è idempotente? Dipende da girc, ma non è garantito. Se `closeConn` è chiamato in un errore fatale e poi il `defer` alla fine richiama `Close`, potrebbe esserci doppia chiusura. Inoltre il `defer` alla fine e il `closeConn` interno sono entrambi non condizionati su `usingExistingConn`, quindi per connessioni persistenti non chiudono, ma per temporanee chiudono due volte su errore.

**Impatto:** Comportamento non definito di girc, potenziale panic o errore.

**Fix consigliata:** Tracciare se `Close` è già stato chiamato con un flag `atomic.Bool` o `sync.Once`. Chiamare `Close` una sola volta per connessione temporanea.

---

### 2.34 `irc.Client`: `reconnect` con `usingExistingConn` non rimuove i vecchi handler se `newClient != c.conn.handlersRegisteredOn` e poi ritorna nil

**File:** `internal/irc/client.go:405-451`

**Problema:** Se `ReconnectCallback` ritorna un nuovo client, `registerHandlers` aggiunge handler. Se poi ritorna di nuovo il vecchio client, il codice confronta i puntatori e riusa, ma i CUIDs sono stati persi? In realtà `c.conn.handlersRegisteredOn` è aggiornato solo quando si registra. Se si riusa un client, i CUIDs rimangono quelli precedenti. Il codice sembra gestire i puntatori, ma la logica di cleanup non è chiara. Se `newClient` è lo stesso puntatore di `c.conn.irc` ma `handlersRegisteredOn` è diverso (impossibile?), i handler non vengono rimossi. Questo è un caso limite.

**Impatto:** Accumulo di handler in scenari di riconnessione particolari.

**Fix consigliata:** Semplificare: quando `usingExistingConn`, chiamare sempre `c.removeHandlers()` sul client corrente prima di registrare su un nuovo client, e tenere traccia dei CUIDs per client. In alternativa, evitare di ri-registrare se il client non è cambiato (come già fatto), ma assicurarsi che i CUIDs siano validi.

---

### 2.35 `api/handlers_server.go` (ipotizzato): non letto, ma `ConnectServerByID` e `DisconnectServerByID` sono esposti via API; non c'è throttling per richieste ripetute

**Impatto:** Spam di connect/disconnect può creare molte goroutine e connessioni parallele.

**Fix consigliata:** Aggiungere un rate limit o un lock per-server nelle API per evitare connect/disconnect ravvicinati.

---

## 3. Bug logici non di concorrenza (ma rilevanti)

### 3.1 `queue.Manager`: `PauseDownload` e `RemoveDownload` non annullano il context del download

**File:** `internal/queue/manager.go:384-419`, `internal/queue/manager.go:433-468`

**Problema:** `PauseDownload` e `RemoveDownload` cancellano `cancelFn` e poi aggiornano lo store. In `PauseDownload` chiamano `cancelFn()` solo se `active`, ma non usano `cancelDownloadWithCtx`. In `RemoveDownload` chiamano `cancelFn()` e poi cancellano lo slot. Il context del download viene annullato, quindi il worker dovrebbe uscire. Tuttavia, se il worker è in `runDownload` e sta copiando il file (operazione lenta), la cancellazione del context non interrompe `io.Copy` o `os.Rename`, quindi il worker può continuare a occupare risorse. `RemoveDownload` cancella il record, ma il worker può comunque finire e poi `completeFn` trova `current == nil` e ritorna. È corretto, ma lente.

**Impatto:** Risposta lenta a pause/remove; risorse occupate più a lungo del necessario.

**Fix consigliata:** Non bloccante, ma considerare di interrompere operazioni I/O con controlli di context o con deadline su file copy.

---

### 3.2 `queue.Manager`: `handleFallback` usa `result.Error` anche dopo che `result` è stato potenzialmente modificato

**File:** `internal/queue/manager.go:893-959`

**Problema:** `handleFallback` legge `result.Error` e `result.Skipped` dopo che `completeFn` ha già processato `result`. `result` è locale al worker e non modificato dopo `completeFn`, quindi è ok. Non è un bug.

---

### 3.3 `irc.Client`: `downloadWithTempConnection` valida `srcPath` con `SafeJoin(cfg.TempDir, srcPath)` ma `srcPath` potrebbe essere già un percorso assoluto

**File:** `internal/queue/worker.go:291-298`

**Problema:** `SafeJoin` probabilmente rifiuta path assoluti. Se `r.FilePath` è assoluto, `SafeJoin` potrebbe restituire errore anche se il path è valido. Dipende dall'implementazione di `SafeJoin` (non letta). Potrebbe essere un falso positivo.

**Impatto:** Download validi falliscono per "invalid temp file path".

**Fix consigliata:** Verificare `SafeJoin`: se rifiuta path assoluti, normalizzare `srcPath` con `filepath.Clean` e controllare che sia sotto `TempDir` con `strings.HasPrefix` o `filepath.Rel`.

---

### 3.4 `ircmanager.Manager`: `Start` itera su `m.cfg.IRC.DefaultServers` e per ogni server chiama `ListServers`, potenzialmente O(N*DB)

**File:** `internal/ircmanager/manager.go:111-167`

**Problema:** Per ogni server auto-connect, `ListServers` è chiamata. Se ci sono molti server, questo è inefficiente. Non è un bug di concorrenza, ma una lentezza che allunga il lock e il tempo di avvio.

**Impatto:** Avvio lento con molti server.

**Fix consigliata:** Caricare la lista una sola volta prima del loop, o usare una query con `address=? AND port=?`.

---

### 3.5 `channellog.Logger`: `sanitizeForLog` sostituisce newline ma non gestisce altri caratteri di controllo (tab, VT, ecc.)

**File:** `internal/channellog/channellog.go:330-338`

**Problema:** I log sono line-based. I caratteri `\x00` o `\x0b` possono corrompere il file o la visualizzazione. Non è un bug di concorrenza.

**Impatto:** Log potenzialmente corrotti.

**Fix consigliata:** Sostituire ogni carattere di controllo non stampabile con spazio, non solo `\r` e `\n`.

---

## 4. Raccomandazioni generali

1. **Aggiungere `go test -race` nella CI** come gate obbligatorio, specialmente per `internal/queue`, `internal/ircmanager`, `internal/irc`, `internal/channellog`, `internal/pubsub`, `internal/sse`.
2. **Introduzione di un flag `closing` atomico** in `queue.Manager` per evitare avvio di nuovi lavori durante lo shutdown.
3. **Rendere `Config` thread-safe** con `sync.RWMutex` o copia su scrittura, dato che viene letto da molte goroutine e scritto da API.
4. **Limitare il numero di file aperti** in `channellog.Logger` e chiudere file inattivi.
5. **Sincronizzare la prenotazione degli slot** in `queue.Manager` nello stesso lock della decisione di dispatch.
6. **Evitare di condividere `*entities.XDCCPack`** tra worker e manager; usare `workerResult` come unico canale di comunicazione.
7. **Verificare i CUIDs in `irc.Client`** e garantire cleanup in ogni percorso di riconnessione.
8. **Aggiungere un test di stress** che avvia 100 download in parallelo e verifica che non ci siano slot duplicati o globalCount negativi.
9. **Rendere `Logger.Close` idempotente** con `sync.Once`.
10. **Documentare il contratto**: `SetIRCManager` deve essere chiamato prima di `Start()`.

---

## 5. Tabella riassuntiva dei bug critici

| # | File | Problema | Severità | Fix chiave |
|---|------|----------|----------|------------|
| 1 | `queue/manager.go:527-621` | `tryDispatch` rilascia lock prima di `startDownload`; possibile doppio dispatch per stesso canale | **Alta** | Prenotare slot atomicamente prima di rilasciare `dispatchMu` |
| 2 | `queue/manager.go:737-753` | Rilascio slot e decremento globalCount non atomici | **Alta** | Unificare sotto `qm.mu` |
| 3 | `queue/manager.go:185-239` | Shutdown può partire nuovi download / eventi persi | **Alta** | Flag `closing` atomico |
| 4 | `queue/manager.go:626+` | Store update con `qm.ctx` cancellato in shutdown | **Media** | Context indipendente per update finali |
| 5 | `ircmanager/manager.go:262-342` | `ConnectServer` può creare doppie connessioni | **Alta** | Inserire in `m.conns` solo dopo setup OK |
| 6 | `ircmanager/manager.go:288-302` | `ConnectServer`/`DisconnectServer` perdono coerenza su `oldConn` | **Alta** | Lock per-server o atomica totale |
| 7 | `channellog/channellog.go:278-307` | `Close` non thread-safe; panic possibile | **Alta** | `sync.Once` o atomic closed |
| 8 | `channellog/channellog.go:309-326` | FD leak senza limite di file aperti | **Media** | Cap/LRU + chiudi inattivi |
| 9 | `config/config.go` | `Config` non thread-safe; data race su reload | **Alta** | RWMutex o copia su scrittura |
| 10 | `irc/client.go:457-459` | `currentPack()` panico su slice vuota | **Media** | Bounds check in `NewClient`/`DownloadAll` |
| 11 | `irc/handlers.go` | Handler usano `currentPack()` a runtime; eventi di pack precedente possono influenzare quello corrente | **Alta** | Catturare pack in ingresso handler |
| 12 | `irc/handlers.go` | `whoisFallbackTimer` non protetto da mutex | **Media** | `sync.Mutex` dedicato |
| 13 | `queue/manager.go:656-665` | Condivisione `*pack` senza sync tra worker e manager | **Alta** | Usare `workerResult` per metadata |
| 14 | `irc/client.go:232-295` | Doppia chiusura di `c.conn.irc` su errori fatali | **Media** | Flag `closed`/`sync.Once` |
| 15 | `ircmanager/manager.go:1262-1300` | `sendChannelGreeting` usa client catturato, potrebbe essere scaduto | **Media** | Rileggere `mc.irc` prima di inviare |

---

## 6. Conclusione

I bug più gravi sono nella gestione della concorrenza di `queue.Manager` (prenotazione slot e shutdown) e `ircmanager.Manager` (connect/disconnect atomici), oltre alla mancanza di thread-safety di `config.Config`. Correggere questi primi 5 punti eliminerebbe i rischi di doppio dispatch, deadlock in shutdown, e data race su configurazione. I bug in `internal/irc` (handler con pack dinamico, timer, chiusura multipla) sono secondari ma possono causare comportamenti errati in download multipli. `channellog.Logger` ha problemi di lifecycle e risorsa che vanno sistemati per produzione stabile.
