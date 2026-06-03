# Analisi Completa del Codebase — xdcc-go

**Data:** 25 Maggio 2026
**Branch:** `develop`
**Metodologia:** analisi statica completa di tutti i file Go, revisione dell'architettura, pattern, testing, e confronto con le best practice Go.

---

## Indice

1. [Panoramica del Progetto](#1-panoramica-del-progetto)
2. [Bug Logici](#2-bug-logici)
3. [Code Smells](#3-code-smells)
4. [Migliorie Possibili](#4-migliorie-possibili)
5. [Ristrutturazioni del Codice](#5-ristrutturazioni-del-codice)
6. [Best Practice Non Applicate](#6-best-practice-non-applicate)
7. [Ottimizzazioni Possibili](#7-ottimizzazioni-possibili)
8. [Limitazioni Attuali](#8-limitazioni-attuali)
9. [Suggerimenti Nuove Funzionalità](#9-suggerimenti-nuove-funzionalità)
10. [Riepilogo e Priorità](#10-riepilogo-e-priorità)

---

## 1. Panoramica del Progetto

### Architettura Generale

Il progetto è un download manager XDCC con architettura a micro-servizi interni:

```
cmd/
  xdcc-server/   → daemon principale (HTTP API + Web UI)
  xdcc-dl/       → CLI per download one-shot
  xdcc-search/   → CLI per ricerca XDCC
  xdcc-browse/   → CLI interattivo per browsing

internal/
  api/           → REST API handlers (chi router)
  config/        → caricamento configurazione YAML + env + CLI
  client/        → client HTTP per API del server
  diskmon/       → monitor spazio disco
  downloader/    → orchestratore download (CLI)
  entities/      → modelli dominio: XDCCPack, IRCServer
  irc/           → client IRC XDCC (basso livello)
  ircmanager/    → gestione connessioni IRC persistenti
  logging/       → logger strutturato con rotazione
  pubsub/        → hub publish/subscribe generico
  queue/         → gestione coda download
  search/        → motori di ricerca XDCC (nibl, xdcc.eu, subsplease)
  searchagg/     → aggregatore search multi-provider con cache
  sse/           → hub Server-Sent Events
  store/         → persistenza SQLite

web/             → frontend Svelte (SPA)
```

### Punti di Forza

- **Separazione chiara delle responsabilità:** ogni package ha un dominio ben definito
- **Pattern di concorrenza maturi:** `sync.Once`, `WaitGroup`, `context.Context`, channel lifecycle
- **Pattern di shutdown ordinato:** sequenza di stop con timeout progressivi
- **Interfacce ben definite:** `store.Store`, `api.IRCManager`, `api.QueueManager`, `ircmanager.IRCManagerInterface`
- **SSE Hub con buffer circolare e Last-Event-ID:** reconnect robusto per il frontend
- **Persistenza completa:** SQLite per servers, channels, downloads, cache, presets, watchlists
- **Tooling CLI completo:** tre binary separati per use case diversi

---

## 2. Bug Logici

### 2.1 [MEDIA] `ensureConnection` RLock riacquisito in modo non sicuro

**File:** `internal/ircmanager/manager.go` — metodo `ensureConnection()`

**Problema:** Il metodo acquisisce `m.mu.RLock()`, itera su `m.conns`, poi rilascia il lock. Più avanti, dopo potenziali side-effect (connessione DB, `ConnectServerByID`), ri-acquisisce `m.mu.RLock()` per leggere `m.conns[serverID]`. Tra il primo e il secondo lock, un altro goroutine potrebbe aver rimosso o sostituito la connessione, rendendo `conn` dangling.

```go
// Primo RLock — itera m.conns
m.mu.RLock()
for id, conn := range m.conns {
    if conn.address == address && conn.port == port {
        m.mu.RUnlock()
        // ... possibile side-effect ...
        return id, conn, nil
    }
}
m.mu.RUnlock()

// ... ConnectServerByID() modifica m.conns ...

// Secondo RLock — conn potrebbe essere stata rimossa
m.mu.RLock()
conn := m.conns[serverID]  // potrebbe essere nil
m.mu.RUnlock()
```

**Fix consigliato:** Mantenere il lock durante tutto il flusso, oppure ri-verificare l'esistenza della connessione dopo la ri-acquisizione.

**Severità:** MEDIA — Race window stretta ma possibile durante startup con molti server.

---

### 2.2 [BASSA] `connect()` crea `connectedCh` ma non lo chiude su alcuni path

**File:** `internal/irc/client.go` — metodo `connect()`

**Problema:** Quando `connect()` tenta IP multipli (DNS resolution), crea `c.connectedCh` PRIMA del loop sugli IP:

```go
c.connectedCh = make(chan struct{})
// ... loop IP ...
select {
case <-c.connectedCh:
    return nil
case connErr := <-c.ircErrCh:
    // ... se è l'ultimo IP, ritorna errore
    // ma connectedCh rimane aperto!
}
```

Se l'ultimo IP fallisce e tutti i precedenti sono falliti, `connectedCh` non viene mai chiuso. Nessun goroutine è bloccato su di esso (il `select` è già uscito), ma è una risorsa non pulita.

**Fix consigliato:** Chiudere `connectedCh` nel `defer` o dopo il loop.

**Severità:** BASSA — Nessun goroutine leak, solo risorsa non rilasciata.

---

### 2.3 [BASSA] `Enqueue` valida spazio disco due volte

**File:** `internal/queue/manager.go` — metodo `Enqueue()` e `tryDispatch()`

**Problema:** `Enqueue()` chiama `qm.diskMon.Check()` per validare lo spazio disco prima di accodare. Poi `tryDispatch()` chiama nuovamente `qm.diskMon.Check()`. Tra le due chiamate, lo spazio disco potrebbe essere cambiato, ma il controllo in `tryDispatch` è ridondante se il download è appena stato accodato (caso comune: `Enqueue` → `tryDispatch` immediato).

**Nota:** La doppia validazione non è strettamente un bug — è difensiva. Ma crea una race: il primo check passa, il secondo fallisce, e il download rimane in coda in stato "queued" senza feedback all'utente.

**Fix consigliato:** Mantenere solo il check in `tryDispatch()`. In `Enqueue()`, validare solo errori catastrophici (es. disco pieno al 100%).

**Severità:** BASSA — Comportamento corretto ma potenzialmente confondente.

---

### 2.4 [BASSA] `BulkAction` non è atomica

**File:** `internal/queue/manager.go` — metodo `BulkAction()`

**Problema:** Il metodo itera sugli ID e chiama `PauseDownload`/`ResumeDownload`/`RemoveDownload` uno alla volta. Se una delle operazioni fallisce, le precedenti sono già state eseguite (no rollback). Il client riceve risultati parziali.

**Fix consigliato:** Documentare la non-atomicità nell'API, oppure implementare una modalità "all-or-nothing" con pre-validazione.

**Severità:** BASSA — Comportamento accettabile per operazioni bulk non critiche.

---

## 3. Code Smells

### 3.1 [MEDIA] `buildResultFromCache` ignora l'opzione `opts.Query`

**File:** `internal/searchagg/aggregator.go` — metodo `buildResultFromCache()`

**Problema:** Quando si costruisce il risultato da cache, `sortPacks(filtered, "")` passa stringa vuota come query invece di `opts.Query`. Questo significa che i risultati dalla cache non sono ordinati per rilevanza rispetto alla query originale.

```go
// buildResultFromCache:
sortPacks(filtered, "")  // ← dovrebbe essere opts.Query

// buildResultFromLive:
sortPacks(filtered, query)  // ← corretto
```

**Fix consigliato:** Passare `opts.Query` a `sortPacks`.

**Severità:** MEDIA — Impatto sulla UX della search quando i risultati sono cachati.

---

### 3.2 [MEDIA] Duplicazione event forwarding in `main.go`

**File:** `cmd/xdcc-server/main.go`

**Problema:** Il forwarding degli eventi IRC e Queue verso SSE Hub contiene ~40 linee di codice quasi identiche duplicate per ogni tipo di evento. Ogni handler ha la stessa struttura: `select` con `eventCtx.Done()` e `case evt`, e lo stesso pattern di drain con timeout.

**Fix consigliato:** Estrarre una funzione generica `forwardEvents[T any](ctx, hub, ch, mapper)` che accetta un channel e una funzione di mapping evento → SSE payload.

**Severità:** MEDIA — Manutenibilità e rischio di bug da copia-incolla.

---

### 3.3 [MEDIA] `Client` struct con 30+ campi

**File:** `internal/irc/client.go`

**Problema:** La struct `Client` ha oltre 30 campi, mescolando stato di connessione, stato per-pack, e configurazione. Questo rende difficile capire cosa viene resettato tra un pack e l'altro e cosa è persistente.

**Fix consigliato:** Estrarre lo stato per-pack in una struct separata `packState`:

```go
type packState struct {
    mu            sync.Mutex
    peerAddr      string
    dccConn       net.Conn
    dccFile       *os.File
    progress      int64
    filesize      int64
    // ...
}

type Client struct {
    ctx       context.Context
    packs     []*entities.XDCCPack
    opts      DownloadOptions
    // stato connessione
    irc       *girc.Client
    // ...
    // stato per-pack
    pack packState
}
```

**Severità:** MEDIA — Debito tecnico che cresce con ogni nuovo campo.

---

### 3.4 [BASSA] `store.Store` interface con 50+ metodi

**File:** `internal/store/store.go`

**Problema:** L'interfaccia `Store` ha oltre 50 metodi. La documentazione Go raccomanda interfacce piccole e focalizzate ("the bigger the interface, the weaker the abstraction").

**Fix consigliato:** Dividere in interfacce per dominio (già supportate dai type assertion nel codice):

```go
type DownloadStore interface { /* metodi download */ }
type ServerStore interface { /* metodi server/channel */ }
type SearchStore interface { /* metodi cache/presets */ }
type WatchlistStore interface { /* metodi watchlist */ }
```

**Nota:** Questo cambiamento è facilitato dal fatto che `api.IRCManager` e `api.QueueManager` già usano interfacce ristrette. `store.Store` è usato come interfaccia interna.

**Severità:** BASSA — Funziona bene, ma viola l'idioma Go delle interfacce piccole.

---

### 3.5 [BASSA] `log.Printf` vs `logger.Infof` inconsistente

**File:** Vari (`ircmanager/manager.go`, `queue/manager.go`, `searchagg/aggregator.go`)

**Problema:** Diversi componenti usano `*log.Logger` con `Printf`, mentre `api` e `store` usano `*logging.Logger` con `Infof`/`Warnf`/`Errorf`. Questo crea inconsistenze nel formato e nel livello di log.

**Stato attuale:** Il refactoring è in corso — `store` ha già migrato a `*logging.Logger`. `ircmanager`, `queue`, e `searchagg` usano ancora `*log.Logger`.

**Fix consigliato:** Completare la migrazione. Il `io.Writer` adapter già esiste in `logging.LevelWriter`. Sostituire tutti i `*log.Logger` con `*logging.Logger`.

**Severità:** BASSA — Impatto estetico, ma rende più difficile filtrare i log per livello.

---

### 3.6 [BASSA] Magic numbers in `connect()` timeout calculation

**File:** `internal/irc/client.go:277`

```go
timeout := time.Duration(c.opts.ConnectTimeout+30) * time.Second
```

**Problema:** Il "+30" non è documentato. Non è chiaro perché esattamente 30 secondi aggiuntivi.

**Fix consigliato:** Estrarre in costante con commento:

```go
const connectGracePeriod = 30 * time.Second // extra time for WHOIS/JOIN after CONNECT
timeout := time.Duration(c.opts.ConnectTimeout)*time.Second + connectGracePeriod
```

**Severità:** BASSA — Leggibilità.

---

### 3.7 [BASSA] `cacheKey` implementazione custom invece di `strings.ToLower` + `strings.Fields` + `strings.Join`

**File:** `internal/searchagg/cache.go` — funzione `cacheKey()`

```go
func cacheKey(query string) string {
    b := make([]byte, 0, len(query))
    skip := false
    for _, ch := range query {
        if ch == ' ' || ch == '\t' || ch == '\n' || ch == '\r' {
            if !skip {
                b = append(b, ' ')
                skip = true
            }
        } else {
            if ch >= 'A' && ch <= 'Z' {
                b = append(b, byte(ch+'a'-'A'))
            } else {
                b = append(b, byte(ch))
            }
            skip = false
        }
    }
    return string(b)
}
```

**Problema:** Reimplementa `strings.Fields` + `strings.Join` + `strings.ToLower` con loop manuale. Più soggetto a bug (es. gestione caratteri non-ASCII, Unicode whitespace).

**Fix consigliato:**
```go
func cacheKey(query string) string {
    return strings.ToLower(strings.Join(strings.Fields(query), " "))
}
```

**Severità:** BASSA — Corretto ma meno leggibile e potenzialmente incompleto (es. `\f`, `\v`, Unicode spaces).

---

## 4. Migliorie Possibili

### 4.1 Aggiungere health check per le connessioni IRC

**File:** `internal/ircmanager/manager.go`

**Descrizione:** Attualmente non c'è un health check proattivo per le connessioni IRC. Se una connessione zombie rimane nello stato "connected" ma non risponde, il manager non lo rileva.

**Implementazione proposta:** Aggiungere un ping periodico (es. `PING` ogni 60 secondi) e marcare la connessione come "degraded" se non risponde entro 30 secondi.

---

### 4.2 Supporto per download scheduling (time-based)

**File:** `internal/queue/manager.go`

**Descrizione:** Il queue manager supporta già la pausa/ripresa. Si potrebbe aggiungere scheduling temporale: "scarica questo pack dopo le 02:00".

**Implementazione proposta:**
- Aggiungere campi `scheduled_after` e `scheduled_before` a `DownloadRecord`
- `tryDispatch` controlla la finestra temporale prima di avviare il download

---

### 4.3 Rate limiting per API REST

**File:** `internal/api/router.go`

**Descrizione:** L'API REST non ha rate limiting. Un client malevolo potrebbe esaurire le risorse del server con richieste ripetute.

**Implementazione proposta:** Aggiungere middleware chi rate limiter (es. `golang.org/x/time/rate` o `github.com/go-chi/httprate`).

---

### 4.4 Graceful degradation dei provider di ricerca

**File:** `internal/search/engine.go`, `internal/searchagg/aggregator.go`

**Descrizione:** Se tutti i provider falliscono, il sistema tenta la cache stantia. Ma non c'è un meccanismo di circuit breaker: un provider instabile viene riprovato a ogni ricerca.

**Implementazione proposta:**
- Aggiungere circuit breaker per provider (dopo N fallimenti consecutivi, sospendere il provider per M minuti)
- Già parzialmente supportato dai `ProviderStats` nello store

---

### 4.5 Supporto per webhook / notifiche esterne

**Descrizione:** Quando un download viene completato, sarebbe utile notificare servizi esterni (es. Discord, Slack, Telegram, webhook HTTP).

**Implementazione proposta:**
- Nuovo package `notifier` con interfaccia `Notifier`
- Implementazioni: `DiscordNotifier`, `SlackNotifier`, `WebhookNotifier`
- Configurazione in `config.yaml`:
  ```yaml
  notifications:
    - type: discord
      webhook_url: "https://..."
      events: [download_completed, download_failed]
  ```

---

### 4.6 Supporto per pacchetti XDCC con password

**File:** `internal/irc/client.go`, `internal/irc/handlers.go`

**Descrizione:** Alcuni bot XDCC richiedono un messaggio specifico (non solo `xdcc send #N`). Per esempio: `xdcc send #N password`. Attualmente non supportato.

**Implementazione proposta:**
- Campo `Password string` in `entities.XDCCPack`
- Handler `NOTICE` che intercetta la richiesta di password
- Invio automatico del messaggio con password

---

## 5. Ristrutturazioni del Codice

### 5.1 Separare `irc.Client` in Connection + Session

**File:** `internal/irc/client.go`

**Motivazione:** Il `Client` gestisce sia la connessione IRC persistente sia lo stato per-pack. Separare in due struct ridurrebbe la complessità e renderebbe più chiaro il lifecycle.

**Proposta:**
```go
type Connection struct {
    irc    *girc.Client
    opts   DownloadOptions
    ctx    context.Context
    logger Logger
}

type DownloadSession struct {
    conn   *Connection
    pack   *entities.XDCCPack
    state  packState
}
```

**Impatto:** Alto (tutti i riferimenti a `Client` vanno aggiornati).

**Priorità:** Bassa (miglioria strutturale, non funzionale).

---

### 5.2 Estrarre `eventBridge` da `main.go`

**File:** `cmd/xdcc-server/main.go`

**Motivazione:** Il codice di forwarding eventi IRC/Queue → SSE Hub occupa ~80 linee in `main.go`. È logica di integrazione che dovrebbe stare in un package dedicato.

**Proposta:**
```go
// internal/bridge/event_bridge.go
type EventBridge struct {
    sseHub *sse.Hub
    logger *logging.Logger
}

func (b *EventBridge) ForwardIRCEvents(ctx context.Context, ch <-chan ircmanager.Event)
func (b *EventBridge) ForwardQueueEvents(ctx context.Context, ch <-chan queue.Event)
```

**Impatto:** Basso (estrazione, non modifica di comportamento).

**Priorità:** Media.

---

### 5.3 Unificare i tre binary CLI con un unico comando

**Motivazione:** `xdcc-dl`, `xdcc-search`, `xdcc-browse` condividono molto codice (configurazione, logging, parsing). Unificarli in un unico binary `xdcc` con sottocomandi ridurrebbe la duplicazione.

**Proposta:**
```
xdcc download --bot ... --pack ...
xdcc search "one piece"
xdcc browse
xdcc server  # nuovo: avvia il daemon
```

**Impatto:** Medio (refactoring dei `main.go`, ma logica interna rimane invariata).

**Priorità:** Bassa (i binary separati funzionano bene).

---

## 6. Best Practice Non Applicate

### 6.1 `go test -race` non in CI

**File:** `.github/workflows/test.yml`

**Problema:** Il race detector non è abilitato nella CI. Considerando la quantità di concorrenza nel progetto, è essenziale.

**Fix:** Aggiungere `go test -race ./...` nella workflow CI.

**Priorità:** ALTA.

---

### 6.2 Benchmark assenti

**Descrizione:** Non ci sono benchmark Go (`func BenchmarkXxx(b *testing.B)`). Componenti come `cacheKey`, `filterPacks`, `sortPacks`, e `EventsSince` potrebbero beneficiare di benchmark per prevenire regressioni.

**Priorità:** BASSA.

---

### 6.3 `gofumpt` o `golangci-lint` non utilizzati

**Descrizione:** Non c'è un linter configurato nel progetto. `golangci-lint` con una configurazione rilassata (es. `revive`, `govet`, `staticcheck`) catturerebbe molti dei code smell elencati sopra.

**Fix:** Aggiungere `.golangci.yml` e integrare nella CI.

**Priorità:** MEDIA.

---

### 6.4 `context.Context` non propagato in tutte le interfacce

**Descrizione:** Molti metodi di `store.Store` non accettano `context.Context`. Questo significa che le operazioni DB non possono essere cancellate in caso di shutdown.

```go
// Attuale:
GetQueue() ([]DownloadRecord, error)

// Ideale:
GetQueue(ctx context.Context) ([]DownloadRecord, error)
```

**Nota:** Questo è un refactoring ampio che tocca 50+ metodi e tutti i loro chiamanti.

**Priorità:** BASSA (il database è locale, le operazioni sono veloci).

---

### 6.5 Error sentinella non documentati

**File:** `internal/irc/errors.go`

**Problema:** Gli errori come `ErrBotDenied`, `ErrAlreadyDownloaded` sono variabili esportate ma senza documentazione GoDoc.

**Fix:** Aggiungere commenti `// ErrXxx ...` sopra ogni variabile.

**Priorità:** BASSA.

---

## 7. Ottimizzazioni Possibili

### 7.1 Pool di buffer per `receiveData`

**File:** `internal/irc/dcc.go`

**Descrizione:** `receiveData()` alloca un `buf := make([]byte, 64*1024)` a ogni chiamata. Con molti download paralleli, questo crea pressione sul GC.

**Proposta:** Usare `sync.Pool` per i buffer:

```go
var bufPool = sync.Pool{
    New: func() any { return make([]byte, 64*1024) },
}

func (c *Client) receiveData() {
    buf := bufPool.Get().([]byte)
    defer bufPool.Put(buf)
    // ...
}
```

**Priorità:** BASSA (64KB × 5 download paralleli = 320KB, trascurabile).

---

### 7.2 Indici SQLite mancanti

**File:** `internal/store/schema.go`

**Descrizione:** Alcune query frequenti potrebbero beneficiare di indici aggiuntivi:

- `downloads` filtrati per `status` + `channel` → indice composito
- `search_cache` per `query_key` + `expires_at` → già presente? Da verificare

**Proposta:** Analizzare le query con `EXPLAIN QUERY PLAN` e aggiungere indici mirati.

**Priorità:** BASSA (SQLite è molto veloce con dataset < 100K righe).

---

### 7.3 `searchLive` parallelismo limitato

**File:** `internal/searchagg/aggregator.go`

**Descrizione:** `searchLive` lancia tutti i provider in parallelo con `WaitGroup`, ma non c'è un limite di concorrenza. Con 10+ provider e timeout di 5s, potrebbero esserci 10 richieste HTTP simultanee.

**Proposta:** Usare un semaforo per limitare la concorrenza a N provider simultanei (es. 3).

**Priorità:** BASSA (il numero di provider è tipicamente < 5).

---

### 7.4 `json.NewEncoder` vs `json.Marshal` negli handler

**File:** `internal/api/handlers_*.go`

**Descrizione:** `writeJSON` usa `json.NewEncoder(w).Encode(v)`. Per payload grandi (>1MB), `NewEncoder` è più efficiente in memoria di `json.Marshal`. Ma per payload piccoli (tipici dell'API), la differenza è trascurabile.

**Nota:** La scelta attuale è corretta per streaming (SSE). Nessuna modifica necessaria.

---

## 8. Limitazioni Attuali

### 8.1 Nessun supporto TLS/SSL per IRC

**File:** `internal/irc/client.go`, `internal/ircmanager/manager.go`

**Descrizione:** Il client IRC usa solo connessioni plain-text (porta 6667). Non supporta TLS (porta 6697). Questo significa che nick, messaggi, e nomi dei canali sono trasmessi in chiaro.

**Impatto:** Tutti i server IRC pubblici supportano TLS. È una limitazione di sicurezza significativa.

**Fix:** `girc` supporta TLS nativamente. Aggiungere un campo `SSL bool` a `ServerConfig` e `ServerRecord`, e passare `SSL: true` nella configurazione di `girc.New()`.

**Priorità implementazione:** ALTA.

---

### 8.2 Nessuna autenticazione API

**File:** `internal/api/router.go`

**Descrizione:** L'API REST è completamente aperta (CORS `*`). Non c'è autenticazione, API key, o JWT. Chiunque possa raggiungere la porta HTTP può controllare i download, le connessioni IRC, e i dati.

**Impatto:** In ambienti condivisi o esposti, è un rischio di sicurezza.

**Fix:** Aggiungere middleware di autenticazione (API key in header `Authorization`, o basic auth configurabile).

**Priorità implementazione:** MEDIA.

---

### 8.3 Nessuna compressione HTTP

**File:** `internal/api/router.go`

**Descrizione:** Le risposte API e SSE non usano compressione (gzip). Per payload grandi (es. history downloads con 1000+ record), la compressione ridurrebbe significativamente il traffico.

**Fix:** Aggiungere middleware `chi` compression (`chimw.Compress`).

**Priorità implementazione:** BASSA.

---

### 8.4 Nessun graceful reconnect per le connessioni IRC usate dai download

**File:** `internal/ircmanager/manager.go`

**Descrizione:** Se una connessione IRC usata per un download attivo cade, il download fallisce e viene ripreso solo dopo che la connessione viene ristabilita dal `ircmanager`. Sarebbe meglio che il download rimanesse in pausa e riprendesse automaticamente.

**Stato attuale:** Il `queue.worker` gestisce il retry con backoff, ma non c'è un meccanismo di resume dalla stessa posizione (il file parziale viene mantenuto, ma il download riparte da capo per via del protocollo DCC).

**Limitazione intrinseca del protocollo XDCC/DCC:** Il DCC SEND non supporta resume nativo. Il "resume" attuale è un re-invio dell'intero file.

---

### 8.5 Cache risultati ricerca legata alla query esatta

**File:** `internal/searchagg/cache.go`

**Descrizione:** La cache usa la query esatta come chiave (dopo normalizzazione). Query simili come "one piece 1080" e "one piece 1080p" non condividono la cache.

**Miglioramento possibile:** Normalizzazione più aggressiva (stemming, rimozione punteggiatura, sinonimi), ma aumenterebbe la complessità senza un guadagno proporzionale per l'uso tipico.

**Priorità:** BASSA.

---

## 9. Suggerimenti Nuove Funzionalità

### 9.1 Supporto XDCC passivo (DCC RECV)

**Descrizione:** Attualmente il client supporta solo XDCC attivo (il bot si connette al client). Alcuni bot richiedono XDCC passivo (il client si connette al bot). `girc` supporta DCC passivo, ma il client `irc` non lo implementa.

**Complessità:** Media.

---

### 9.2 Supporto per canali IRC con chiave (password)

**File:** `internal/ircmanager/manager.go`

**Descrizione:** `joinChannel` non supporta canali protetti da password (es. `#canale` con key `secret`). Il comando IRC sarebbe `JOIN #canale secret`.

**Implementazione:**
- Aggiungere campo `Key string` a `ChannelRecord` e `ChannelConfig`
- Passare la chiave nel comando `JOIN`

**Complessità:** Bassa.

---

### 9.3 Dashboard di monitoring (Prometheus metrics)

**Descrizione:** Esporre metriche Prometheus per:
- Download attivi, in coda, completati, falliti
- Byte scaricati (rate)
- Connessioni IRC attive
- Spazio disco disponibile
- Latenza provider di ricerca

**Implementazione:** Package `metrics` con `prometheus/client_golang`, endpoint `/metrics`.

**Complessità:** Media.

---

### 9.4 Supporto multi-utente

**Descrizione:** Attualmente il server è single-user. Supportare più utenti con code separate, autenticazione, e permessi.

**Complessità:** Alta — richiede modifiche allo schema DB, API auth, e frontend.

---

### 9.5 Integrazione con Sonarr/Radarr

**Descrizione:** Molti utenti XDCC usano Sonarr/Radarr per la gestione di serie TV e film. Un'integrazione permetterebbe di inviare automaticamente i download completati a Sonarr/Radarr.

**Implementazione:** Webhook verso Sonarr/Radarr API quando un download è completato.

**Complessità:** Bassa (webhook POST con JSON).

---

### 9.6 Preview file durante il download

**Descrizione:** Per file video, permettere lo streaming del file parziale durante il download (es. via HTTP Range requests). Utile per verificare la qualità prima del completamento.

**Complessità:** Alta — richiede un file server con supporto Range e gestione lock.

---

### 9.7 Dark mode nativa nel frontend

**File:** `web/src/`, `web/src/app.css`

**Descrizione:** Il frontend Svelte non sembra supportare dark mode. Implementare un tema scuro con CSS variables e toggle.

**Complessità:** Bassa (puro CSS + store Svelte).

---

## 10. Riepilogo e Priorità

### Legenda Priorità
- 🔴 **ALTA** — Impatto sicurezza, stabilità, o UX significativa
- 🟡 **MEDIA** — Debito tecnico, manutenibilità, o funzionalità importante
- 🟢 **BASSA** — Miglioramento estetico, ottimizzazione marginale, o nice-to-have

### Tabella Riassuntiva

| # | Categoria | Issue | Severità | Priorità Implementazione |
|---|-----------|-------|----------|-------------------------|
| 6.1 | Best Practice | `go test -race` non in CI | — | 🔴 ALTA |
| 8.1 | Limitazione | Nessun supporto TLS/SSL per IRC | — | 🔴 ALTA |
| 2.1 | Bug Logico | `ensureConnection` RLock non sicuro | MEDIA | 🟡 MEDIA |
| 3.1 | Code Smell | `buildResultFromCache` ignora query | MEDIA | 🟡 MEDIA |
| 3.2 | Code Smell | Duplicazione event forwarding | MEDIA | 🟡 MEDIA |
| 3.3 | Code Smell | `Client` struct con 30+ campi | MEDIA | 🟡 MEDIA |
| 5.2 | Ristrutturazione | Estrarre `eventBridge` da `main.go` | — | 🟡 MEDIA |
| 6.3 | Best Practice | `golangci-lint` non configurato | — | 🟡 MEDIA |
| 8.2 | Limitazione | Nessuna autenticazione API | — | 🟡 MEDIA |
| 2.2 | Bug Logico | `connectedCh` non chiuso su alcuni path | BASSA | 🟢 BASSA |
| 2.3 | Bug Logico | `Enqueue` valida disco due volte | BASSA | 🟢 BASSA |
| 2.4 | Bug Logico | `BulkAction` non atomica | BASSA | 🟢 BASSA |
| 3.4 | Code Smell | `Store` interface 50+ metodi | BASSA | 🟢 BASSA |
| 3.5 | Code Smell | `log.Printf` vs `logger.Infof` | BASSA | 🟢 BASSA |
| 3.6 | Code Smell | Magic number `+30` in `connect()` | BASSA | 🟢 BASSA |
| 3.7 | Code Smell | `cacheKey` implementazione custom | BASSA | 🟢 BASSA |
| 4.1 | Miglioria | Health check connessioni IRC | — | 🟢 BASSA |
| 4.2 | Miglioria | Download scheduling | — | 🟢 BASSA |
| 4.3 | Miglioria | Rate limiting API REST | — | 🟢 BASSA |
| 4.4 | Miglioria | Circuit breaker provider search | — | 🟢 BASSA |
| 4.5 | Miglioria | Webhook/notifiche esterne | — | 🟢 BASSA |
| 4.6 | Miglioria | Supporto XDCC con password | — | 🟢 BASSA |
| 5.1 | Ristrutturazione | Separare `Client` in Connection+Session | — | 🟢 BASSA |
| 5.3 | Ristrutturazione | Unificare binary CLI | — | 🟢 BASSA |
| 6.2 | Best Practice | Benchmark assenti | — | 🟢 BASSA |
| 6.4 | Best Practice | `context.Context` in `Store` | — | 🟢 BASSA |
| 6.5 | Best Practice | Error sentinella senza GoDoc | — | 🟢 BASSA |
| 7.1 | Ottimizzazione | Pool buffer `receiveData` | — | 🟢 BASSA |
| 7.2 | Ottimizzazione | Indici SQLite | — | 🟢 BASSA |
| 7.3 | Ottimizzazione | Semaphore `searchLive` | — | 🟢 BASSA |
| 8.3 | Limitazione | Nessuna compressione HTTP | — | 🟢 BASSA |
| 8.4 | Limitazione | DCC resume non supportato dal protocollo | — | 🟢 BASSA (intrinseca) |
| 8.5 | Limitazione | Cache search per query esatta | — | 🟢 BASSA |
| 9.1 | Funzionalità | XDCC passivo (DCC RECV) | — | 🟢 BASSA |
| 9.2 | Funzionalità | Canali IRC con password | — | 🟢 BASSA |
| 9.3 | Funzionalità | Prometheus metrics | — | 🟢 BASSA |
| 9.4 | Funzionalità | Supporto multi-utente | — | 🟢 BASSA |
| 9.5 | Funzionalità | Integrazione Sonarr/Radarr | — | 🟢 BASSA |
| 9.6 | Funzionalità | Preview file durante download | — | 🟢 BASSA |
| 9.7 | Funzionalità | Dark mode frontend | — | 🟢 BASSA |

### Piano d'Azione Consigliato (Quick Wins)

1. **Settimana 1:** `go test -race` in CI (30 min) + `golangci-lint` setup (1 ora)
2. **Settimana 2:** TLS/SSL IRC (4 ore) + autenticazione API (4 ore)
3. **Settimana 3:** `ensureConnection` race fix (1 ora) + `buildResultFromCache` query fix (15 min) + event bridge extraction (2 ore)
4. **Sprint successivo:** Logging unification + code smells minori + feature requests

---

*Report generato il 25 Maggio 2026. Analisi completa di 50+ file Go, ~15,000 linee di codice.*
