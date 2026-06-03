# Channel Auto-Discovery

## Problema
Nelle ricerche XDCC, l'informazione sul canale IRC spesso non è disponibile nella risposta dei provider. Questo rendeva necessario specificare manualmente un canale prima di avviare un download.

## Soluzione Implementata
Il sistema ora implementa la scoperta automatica del canale tramite WHOIS IRC:

### Flusso del Download con Auto-Discovery

1. **Ricerca**: L'utente cerca un pack XDCC tramite i provider (NIBL, IXIRC, SubsPlease, XDCC.eu)
   - Il risultato può contenere o meno il canale
   - Il frontend non richiede più il canale come campo obbligatorio

2. **Enqueue Download**: Quando l'utente clicca "Download":
   - Il canale viene passato se disponibile nei risultati di ricerca
   - Se non disponibile, viene passato come stringa vuota
   - Il `pack_message` viene generato automaticamente nel formato `xdcc send #<numero>`

3. **Avvio Download**:
   - Il sistema si connette al server IRC
   - Aspetta un delay casuale (5-10 secondi) per evitare rate limits
   - Invia un comando WHOIS per il bot specificato

4. **WHOIS Response**:
   - Il server IRC risponde con tutti i canali in cui il bot è presente
   - Il client riceve il messaggio `RPL_WHOISCHANNELS` con la lista dei canali

5. **Join Automatico**:
   - Se il bot è in un solo canale, quello viene automaticamente joinato
   - Se il bot è in più canali, vengono joinati tutti (con delay per evitare flood)
   - Se il bot non è in nessun canale e c'è un fallback channel configurato, viene usato quello
   - Se non ci sono canali scoperti e nessun fallback, la richiesta viene inviata direttamente

6. **Richiesta XDCC**:
   - Dopo il join (o se il canale era già joinato), viene inviata la richiesta XDCC
   - Il bot risponde con un DCC SEND
   - Il download parte

7. **Persistenza della Connessione**:
   - La connessione IRC rimane attiva tra diversi download
   - I canali già joinati non vengono ri-joinati
   - Questo migliora le performance e riduce il carico sui server IRC

## Modifiche al Codice

### Backend

1. **`internal/api/handlers_download.go`**:
   - ✅ Rimosso il vincolo che rendeva obbligatorio il campo `channel`
   - ✅ Ora il channel è opzionale e può essere stringa vuota

2. **`internal/queue/manager.go`**:
   - ✅ Rimossa la validazione che richiedeva un channel non vuoto
   - ✅ Aggiornato commento per chiarire che il channel può essere vuoto
   - ✅ Il channel vuoto viene passato come `FallbackChannel` al client IRC

3. **`internal/queue/worker.go`**:
   - ✅ Aggiornato commento per chiarire che il channel può essere vuoto
   - ✅ Il channel vuoto viene passato come `FallbackChannel` al client IRC

4. **`internal/irc/handlers.go`**:
   - ✅ Aggiornato commento per evidenziare che se il bot è in un singolo canale, viene automaticamente joinato

5. **`internal/searchagg/filters.go`**:
   - ✅ Modificato ordinamento risultati per essere alfabetico per nome file

### Frontend

1. **`web/src/components/Search.svelte`**:
   - ✅ Modificato `downloadPack()` per generare automaticamente il `pack_message`
   - ✅ Il channel viene lasciato vuoto se non disponibile, permettendo al WHOIS di scoprirlo
   - ✅ Formato pack_message: `xdcc send #<pack_number>`

## Vantaggi

- ✅ **Nessuna interazione utente richiesta**: Il canale viene scoperto automaticamente
- ✅ **Supporto per bot multi-canale**: Vengono joinati tutti i canali in cui il bot è presente
- ✅ **Compatibilità con provider senza channel info**: Funziona anche quando i provider non forniscono il canale
- ✅ **Performance migliorate**: Connessioni persistenti e canali già joinati non vengono ri-joinati
- ✅ **Resilienza**: Fallback channel disponibile per casi edge
- ✅ **Risultati ordinati alfabeticamente**: Più facile trovare i file nella lista

## Test

Per testare la funzionalità:

1. Avvia il server: `./xdcc-server.exe`
2. Apri il browser su `http://localhost:8080`
3. Vai alla sezione "Search"
4. Fai una ricerca (es. "ubuntu")
5. Clicca su "Download" su un risultato
6. Controlla i log del server - dovresti vedere:
   ```
   Sending WHOIS for bot '<bot_name>'
   WHOIS channels: <lista_canali>
   Joining channel <channel_name>
   Joined channel: <channel_name>
   Sending XDCC request: /msg <bot> xdcc send #<pack>
   ```
7. Il download dovrebbe partire automaticamente

## Note Tecniche

- Il WHOIS viene sempre eseguito all'inizio del download, anche se il channel è specificato
- Questo permette di scoprire altri canali in cui il bot è presente
- I canali già joinati in una sessione precedente vengono riconosciuti e non ri-joinati
- Il sistema implementa delay casuali per evitare ban per flood
- Il `pack_message` è generato lato frontend per garantire il formato corretto
- La validazione del channel è stata rimossa sia dall'API che dal QueueManager
