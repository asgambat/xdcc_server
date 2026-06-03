# SSE vs WebSocket: Analisi Comparativa e Decisione

## 🔍 Diagnosi Problema SSE Attuale

### Evidenza dai Log
```
2026/05/20 12:42:02 GET /api/events ...
2026/05/20 12:42:52 GET /api/events 200 49.744s
```

**La connessione SSE si chiude dopo ~50 secondi!**

### Possibili Cause
1. **Timeout di rete/proxy non identificato** - Qualche layer tra client e server chiude la connessione
2. **Problema con Flush()** - Il keepalive potrebbe non essere effettivamente inviato
3. **Context cancellation** - Il request context potrebbe essere cancellato prematuramente
4. **Channel closure** - Il channel del SSE Hub potrebbe essere chiuso per errore

### Impatto
- ❌ Ciclo infinito di reconnessioni SSE (ogni 50s)
- ❌ Saturazione goroutine del server
- ❌ Richieste HTTP rimangono pending (server overload)
- ❌ Search e altre API non rispondono

---

## ⚖️ SSE vs WebSocket: Confronto Dettagliato

### 📡 Server-Sent Events (SSE) - Situazione Attuale

#### ✅ PRO
1. **Semplicità implementativa**
   - Browser API nativa: `new EventSource(url)`
   - No librerie esterne necessarie (client-side)
   - Automatic reconnection built-in del browser
   - Last-Event-ID support nativo per recovery

2. **Compatibilità HTTP**
   - Funziona su HTTP/1.1 standard
   - Passa attraverso proxy HTTP tradizionali
   - Non richiede upgrade della connessione
   - Compatibile con reverse proxy (nginx, Apache)

3. **Architettura semplice**
   - Request unidirezionale (server → client)
   - Nessun protocollo custom da gestire
   - Text-based (facile debugging con curl)
   - Event-driven con named events

4. **Codice già implementato**
   - Hub SSE completo con buffer events
   - Last-Event-ID support per reconnection
   - Frontend già integrato
   - ~500 righe di codice funzionante

#### ❌ CONTRO
1. **Unidirezionale**
   - Solo server → client
   - Client deve usare REST API per inviare comandi
   - Maggior latenza per azioni interattive

2. **Connessione persistente problematica**
   - Timeout non configurabili a livello applicativo
   - Proxy/gateway possono chiudere la connessione
   - Necessita keepalive aggressivo
   - **BUG ATTUALE: connessioni chiuse dopo 50s**

3. **Limiti browser**
   - Chrome: max 6 connessioni HTTP/1.1 per domain
   - EventSource occupa 1 slot persistente
   - Può causare connection pool saturation

4. **Debugging complesso**
   - Difficile identificare perché una connessione si chiude
   - Browser reconnect automatico nasconde i problemi
   - No visibilità su timeout di rete intermediari

---

### 🔌 WebSocket - Alternativa

#### ✅ PRO
1. **Bidirezionale full-duplex**
   - Client e server possono inviare messaggi in qualsiasi momento
   - Latenza inferiore per azioni interattive
   - Singola connessione per tutti i messaggi

2. **Protocollo dedicato**
   - Connessione upgrade HTTP → WS
   - Framing protocol efficiente
   - Supporto nativo per ping/pong (keepalive)
   - Controllo completo su timeouts

3. **Scalabilità**
   - Overhead minore per messaggio
   - Binary e text frames
   - Compressione nativa (permessage-deflate)

4. **Librerie mature**
   - Go: `gorilla/websocket` (standard de-facto)
   - Browser: WebSocket API nativa
   - Reconnection logic più controllabile

#### ❌ CONTRO
1. **Complessità implementativa**
   - Protocollo custom da definire (message types, routing)
   - Gestione manuale della reconnection (no auto-reconnect)
   - Serializzazione/deserializzazione messaggi
   - Error handling più complesso

2. **Compatibilità limitata**
   - Alcuni proxy/firewall bloccano WS
   - Requires protocol upgrade support
   - Problemi con alcuni reverse proxy mal configurati

3. **Refactoring completo**
   - Eliminare SSE Hub (~200 righe)
   - Riscrivere handlers API (~100 righe)
   - Refactoring frontend (~150 righe)
   - Testing completo del nuovo sistema
   - **Stimato: 3-5 giorni di lavoro**

4. **Gestione connessione manuale**
   - Implementare retry logic con exponential backoff
   - Gestire graceful shutdown
   - Implementare message buffering
   - Gestire connection state machine

---

## 🛠️ Difficoltà Fix Bug SSE

### Opzione 1: Debug del problema corrente (1-2 giorni)

**Azioni:**
1. Aggiungere logging dettagliato nell'handler SSE:
   ```go
   case <-r.Context().Done():
       log.Printf("SSE: context canceled: %v", r.Context().Err())
   case <-keepalive.C:
       log.Printf("SSE: sending keepalive")
   ```

2. Testare con curl per isolare il problema:
   ```bash
   curl -N http://localhost:8080/api/events
   ```

3. Verificare Flush() funziona correttamente:
   ```go
   if err := flusher.Flush(); err != nil {
       log.Printf("SSE: flush error: %v", err)
   }
   ```

4. Ridurre intervallo keepalive (30s → 15s)

5. Configurare timeout HTTP server espliciti:
   ```go
   srv := &http.Server{
       ReadTimeout:  0, // No timeout for SSE
       WriteTimeout: 0, // No timeout for SSE
       IdleTimeout:  120 * time.Second,
   }
   ```

**Probabilità successo:** 70%
**Rischio:** Se il problema è un proxy/gateway esterno, potrebbe essere irrisolvibile

### Opzione 2: Implementare retry logic robusto frontend (4-8 ore)

Anche se SSE si disconnette, impedire il loop infinito:

```javascript
class RobustSSEClient {
  constructor() {
    this.reconnectDelay = 1000;
    this.maxReconnectDelay = 30000;
    this.reconnectAttempts = 0;
  }

  connect() {
    this.eventSource = new EventSource('/api/events');
    
    this.eventSource.onopen = () => {
      this.reconnectDelay = 1000; // Reset
      this.reconnectAttempts = 0;
    };

    this.eventSource.onerror = () => {
      this.eventSource.close();
      this.reconnectAttempts++;
      
      // Exponential backoff
      const delay = Math.min(
        this.reconnectDelay * Math.pow(2, this.reconnectAttempts),
        this.maxReconnectDelay
      );
      
      setTimeout(() => this.connect(), delay);
    };
  }
}
```

**Probabilità successo:** 95%
**Pro:** Mitiga il problema anche se non risolve la causa root

### Opzione 3: Migrazione completa a WebSocket (3-5 giorni)

Vedi sezione precedente.

---

## 🎯 Raccomandazione

### ✅ RACCOMANDAZIONE: Fix SSE + Implementare Retry Robusto

**Motivazione:**
1. **Codice già funzionante** - 500+ righe già scritte e testate
2. **SSE è adeguato** - Per questo use case (server → client prevalente)
3. **Fix stimato rapido** - 1-2 giorni vs 5 giorni WebSocket
4. **Retry mitiga il problema** - Anche se il bug non è risolvibile al 100%
5. **WebSocket è overkill** - Non serve bidirezionalità full-duplex per notifiche
6. **Raspberry Pi limitation** - WebSocket usa più risorse (goroutine per messaggi)

**Piano di azione suggerito:**
1. **Fase 1 (4h):** Implementare exponential backoff frontend
2. **Fase 2 (8h):** Debug SSE con logging dettagliato
3. **Fase 3 (4h):** Testing e tuning keepalive/timeouts
4. **Fase 4 (opzionale):** Se SSE non risolvibile, considerare WS

### ⚠️ Quando Considerare WebSocket

Considera WebSocket SE:
- ❌ Fix SSE fallisce dopo 2-3 giorni di debugging
- ❌ Il problema è irrisolvibile (proxy/gateway esterno)
- ✅ Servono comandi real-time client → server (es. streaming chat)
- ✅ Volume di eventi molto alto (> 100 eventi/sec)

---

## 📋 Risorse Necessarie

### Per Fix SSE
- ✅ Go: nessuna libreria aggiuntiva
- ✅ Frontend: nessuna libreria aggiuntiva
- ✅ Costi: 0€
- ⏱️ Tempo: 1-2 giorni

### Per WebSocket
- 📦 Go: `github.com/gorilla/websocket` (~200KB)
- 📦 Frontend: Possibilmente wrapper (SockJS, reconnecting-websocket)
- ⏱️ Tempo: 3-5 giorni
- 🧪 Testing: Completo refactoring richiede test estensivi

---

## 🔧 Prossimi Step Proposti

1. **Implementare retry robusto frontend** (IMMEDIATO - 4h)
   - Exponential backoff
   - Max reconnect delay 30s
   - Logging reconnection attempts

2. **Aggiungere diagnostic logging SSE** (IMMEDIATO - 2h)
   - Log quando/perché handler termina
   - Log keepalive inviati
   - Log flush errors

3. **Testing isolato** (8h)
   - curl test SSE (no browser)
   - Verificare Flush() funziona
   - Ridurre keepalive a 15s
   - Testare su Raspberry Pi (ARM64)

4. **Decision point** (dopo 48h)
   - Se SSE stabile → Chiudere issue
   - Se SSE instabile → Valutare migrazione WebSocket

