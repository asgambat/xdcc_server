# Piano Refactoring IRC Manager Integration

## Obiettivo
Integrare l'IRC Manager con il sistema di download in modo che:
1. I download dal server web usino connessioni persistenti
2. Le connessioni rimangano attive dopo i download
3. Si risparmino 10-20 secondi per download evitando connessioni/disconnessioni

## Architettura

### Prima (Attuale)
```
Server Web → Queue Manager → Worker → irc.NewClient() → IRC temporaneo
                                                          ↓
                                                          Connetti
                                                          ↓
                                                          Download
                                                          ↓
                                                          Disconnetti
```

### Dopo (Target)
```
Server Web → Queue Manager → IRC Manager (connessioni persistenti)
                             ↓
                             Connetti se necessario
                             ↓
                             WHOIS bot → trova canale
                             ↓
                             Join canale se necessario  
                             ↓
                             Invia richiesta XDCC
                             ↓
                             Download DCC
                             ↓
                             Mantieni connessione
```

## Step Implementazione

### Step 1: Estendere IRC Manager
File: `internal/ircmanager/manager.go`

Aggiungere metodi:
- `DownloadPack(ctx, pack, channel, progressFn) (filePath, error)`
  - Connetti al server se non connesso
  - WHOIS sul bot se channel vuoto
  - Join canale se necessario
  - Invia richiesta XDCC al bot
  - Gestisci DCC transfer con progress callback
  - Salva file in TempDir
  - Ritorna path del file scaricato
  - Mantieni connessione attiva

### Step 2: Modificare Queue Manager
File: `internal/queue/manager.go`

- Aggiungere campo `ircMgr IRCManagerInterface`
- Aggiungere metodo `SetIRCManager(ircMgr)`
- Passare `ircMgr` al worker config

### Step 3: Modificare Worker
File: `internal/queue/worker.go`

- Aggiungere campo `IRCManager` in `DownloadConfig`
- In `runDownload()`:
  ```go
  if cfg.IRCManager != nil {
      // Usa IRC Manager (persistente)
      filePath, err = cfg.IRCManager.DownloadPack(...)
  } else {
      // Fallback a connessione temporanea (per CLI)
      filePath, err = downloadWithTempConnection(...)
  }
  ```

### Step 4: Integrare in main.go
File: `cmd/xdcc-server/main.go`

```go
// After creating IRC Manager
ircMgr := ircmanager.New(st, cfg, logger)
ircMgr.Start()

// After creating Queue Manager  
queueMgr := queue.New(st, cfg, logger)
queueMgr.SetIRCManager(ircMgr)  // ← NUOVO
queueMgr.Start()
```

## Complessità
- IRC Manager extension: ALTA - richiede gestione DCC con connessioni persistenti
- Queue Manager: BASSA - solo aggiungere campo e passarlo
- Worker: MEDIA - logica condizionale + fallback
- Integration: BASSA - una riga

## Alternative più semplici (se troppo complesso)
Se l'implementazione completa richiede troppo tempo, alternativa più semplice:

1. **Caching connessioni IRC**: Mantenere un pool di connessioni IRC riutilizzabili
   - Pro: Più semplice da implementare
   - Contro: Non integrato con UI, canali non visibili

2. **Connection reuse nella stessa sessione**: Riusare connessioni per download sequenziali sullo stesso server
   - Pro: Molto semplice
   - Contro: Solo risparmio parziale

## Prossimi Step
1. Creare metodo `DownloadPack` in IRCManager (più complesso)
2. Modificare Worker per usare IRCManager se disponibile (semplice)
3. Modificare main.go per collegare IRC Manager e Queue Manager (una riga)
