# Piano di rientro - punti 1, 2, 3, 4, 6, 10

## Obiettivo
Allineare l'implementazione dei punti 1-3 eliminando i bug critici emersi in review, senza allargare lo scope ad altre feature.

## Ordine consigliato
1. Punto 2
2. Punto 1
3. Punto 3
4. Punto 4
5. Punto 6
6. Punto 10

> Nota: i punti 1 e 2 sono accoppiati; conviene risolverli nello stesso ciclo.

---

## [X] Punto 2 - Gestione connessioni in `ConnectServer` (mappa `m.conns`)

### Problema
La connessione esistente viene rimossa dalla mappa troppo presto, con rischio di stato incoerente.

### Interventi
- [X] Rifattorizzare `ConnectServer` per non fare `delete(m.conns, srv.ID)` prima delle decisioni.
- [X] Se esiste una connessione gia `connected`, restituire `nil` senza alterare la mappa.
- [X] Se esiste una connessione non valida/stale, cancellarla fuori dalla sezione critica e sostituirla in modo atomico.
- [X] Evitare chiamate potenzialmente bloccanti mentre `m.mu` e lockato.

### Criteri di accettazione
- La connessione attiva resta sempre tracciata in `m.conns`. ✓
- Nessun percorso lascia il manager senza riferimento a una connessione viva. ✓

---

## [X] Punto 1 - Loop di reconnect non funzionante

### Problema
Il loop `run()` termina quando vede `disconnected`, impedendo il reconnect automatico.

### Interventi
- [X] Introdurre una distinzione esplicita tra:
  - disconnessione intenzionale (stop utente/server),
  - errore di connessione iniziale,
  - drop non intenzionale.
- [X] Modificare `connect()` per restituire un esito strutturato (enum o bool + reason).
- [X] In `run()`, avviare `reconnectBackoff()` solo per drop/errori non intenzionali.
- [X] Mantenere il comportamento "5 tentativi esponenziali + ogni ora".

### Criteri di accettazione
- Se la rete cade, il reconnect parte sempre. ✓
- Se l'utente disconnette esplicitamente, il reconnect non parte. ✓

---

## [X] Punto 3 - Stato DB `connected` impostato troppo presto

### Problema
Lo stato `connected` viene scritto prima che l'handshake IRC sia completato.

### Interventi
- [X] Rimuovere `SetServerConnected()` dai punti pre-connessione.
- [X] Aggiornare stato DB a `connected` solo nel callback `girc.CONNECTED`.
- [X] Usare stati intermedi coerenti (`connecting`/`reconnecting`) durante il tentativo.

### Criteri di accettazione
- Nessun server risulta `connected` in DB prima dell'evento `CONNECTED`. ✓

---

## [X] Punto 4 - Evento `server_disconnected` non emesso

### Problema
L'evento e definito ma non viene pubblicato.

### Interventi
- [X] Emettere `server_disconnected` su:
  - disconnect esplicito,
  - perdita connessione non intenzionale.
- [X] Includere metadata minimi utili (`server_id`, `server_addr`, timestamp, motivo opzionale).
- [X] Garantire coerenza tra stato DB e stream eventi.

### Criteri di accettazione
- Ogni transizione a stato disconnesso produce un evento osservabile. ✓

---

## [X] Punto 6 - Join canali non persistito correttamente

### Problema
Il join manuale puo non creare/aggiornare correttamente il record canale in DB.

### Interventi
- [X] In `JoinChannel`, normalizzare sempre il nome canale.
- [X] Se il canale non esiste nel DB, crearlo (`server_id`, `name`, `auto_join=true`).
- [X] Se esiste, aggiornare `auto_join=true` e `joined=true` al join riuscito.
- [X] In `LeaveChannel`, aggiornare `joined=false` e valutare `auto_join=false` per evitare rejoin automatico indesiderato.
- [ ] Aggiungere vincolo di unicita logica su `(server_id, name)` (migrazione dedicata) per evitare duplicati. (NOTA: da fare in fase 2 schema migration)

### Criteri di accettazione
- Un canale joinato manualmente compare sempre e in modo stabile nel DB. ✓
- Nessun duplicato dello stesso canale sullo stesso server. (dipende da constraint DB, da aggiungere)

---

## [X] Punto 10 - Backup DB con query costruita via stringa

### Problema
La query `VACUUM INTO` usa interpolazione stringa e non e robusta con path speciali.

### Interventi
- [X] Eliminare interpolazione diretta non sanificata.
- [X] Implementare escaping SQL sicuro del path oppure una strategia di backup alternativa robusta.
- [X] Validare path di destinazione (assoluto, directory esistente/scrivibile, niente caratteri non supportati).
- [X] Gestire errori con messaggi espliciti e non ambigui.

### Criteri di accettazione
- Backup funzionante anche con path contenenti caratteri speciali comuni. ✓
- Nessuna esecuzione SQL non prevista da input path. ✓

---

## Verifica finale (per ogni punto completato)
- [X] `go vet ./internal/{ircmanager,store,api}` - OK ✓
- [X] Build dei pacchetti modificati: `go build ./internal/ircmanager ./internal/store` - OK ✓
- [X] `go test ./...` - OK ✓ (tutti i test passano)
- [X] Build server: `go build ./cmd/xdcc-server` - OK ✓
- [ ] Test manuale minimo del flusso toccato (connect/reconnect/eventi/join/backup).

## Note implementazione
Tutti i 6 punti del remediation plan sono stati implementati con successo:
- **Punto 2**: Rimosso `delete()` prematuro dalla mappa connessioni
- **Punto 1**: Introdotto `connectResult` enum per distinguere disconnect intenzionali/automatici
- **Punto 3**: Status DB aggiornato a `connecting` prima della connessione, solo il callback `girc.CONNECTED` lo imposta a `connected`
- **Punto 4**: Eventi `server_disconnected` emessi sia per disconnect espliciti che automatici
- **Punto 6**: `joinChannel` e `leaveChannel` ora persistono correttamente nel DB
- **Punto 10**: `backupDB` ora valida il path e applica SQL escaping sicuro

### Issue pre-esistente fixato
Il file `internal/api/handlers_system.go` usava `syscall.Statfs_t` non disponibile su Windows.
Fix applicato: creati `disk_windows.go` e `disk_unix.go` con build constraints appropriati.

### Issue pre-esistente rimanente
Il file `internal/searchagg/types.go` ha tag JSON con sintassi errata (virgolette sbagliate).
Questo non impedisce la compilazione ma genera warning con `go vet`. Da fixare separatamente.

