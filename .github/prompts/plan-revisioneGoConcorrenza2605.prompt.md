## Plan: Revisione Go Concorrenza 26-05

Produrre una review tecnica critica e strutturata del progetto Go, basata su analisi read-only del codice, senza eseguire test/build/applicazioni. Il risultato finale sarà un report Markdown nella cartella docs/plan_and_review con problemi classificati per categoria, severità, spiegazione, proposta di fix e note architetturali.

**Steps**
1. Fase 1 - Consolidamento evidenze (base del report)
2. Raccogliere e deduplicare i findings già emersi nelle aree core concorrenti evidenziati nei documenti che trovi nella cartella docs/plan_and_review/ e verifica se sono ancora presenti o risolti per poi integrarli con nuova analisi statica dei sorgenti elencati. Dipendenza: nessuna.
3. Verificare i finding ad alta priorità con lettura diretta dei sorgenti per confermare linee e comportamento reale (in particolare channel close/send, timeout, waitgroup, handler lifecycle).'Se un file elencato non è accessibile, segnalarlo nel report come finding non verificabile e procedere con i file disponibili. Dipende dal passo precedente.
4. Fase 2 - Strutturazione della review per obiettivi richiesti
5. Organizzare i risultati nelle 6 categorie richieste: Concorrenza e Goroutine; Bug subdoli di concorrenza; Memory e Resource Leak; Code Quality e Smell; Best Practices Go; Performance. Dipende da Fase 1.
6. Per ogni issue includere severità (Alta/Media/Bassa), impatto pratico, perché è subdolo/intermittente quando applicabile, e una proposta concreta di fix/refactoring. Parallelizzabile per categoria dopo consolidamento.
7. Fase 3 - Composizione report Markdown finale
8. Creare il documento in docs/plan_and_review con naming richiesto go_review_26_05_2026.md e contenuto completo della review.
9. Inserire sezione iniziale con metodo e vincoli della review (read-only, nessuna esecuzione) e sezione finale con suggerimenti architetturali prioritizzati (quick wins vs interventi strutturali). Dipende da Fase 2.
10. Fase 4 - QA editoriale del documento
11. Verificare consistenza severità, assenza duplicati, correttezza riferimenti file/linea e allineamento ai 6 obiettivi del prompt. Dipende da Fase 3.
12. Verificare completezza output richiesto: elenco strutturato, spiegazione chiara, esempio fix/refactoring, suggerimenti architetturali. Dipende da passo precedente.

**Relevant files**
- c:/progetti/altro/xdcc-go/internal/queue/manager.go — verificare startup delay timer lifecycle, shutdown sequencing, monitor loop.
- c:/progetti/altro/xdcc-go/internal/irc/handlers.go — verificare timeout goroutine e possibili leak legati a canali di completamento download.
- c:/progetti/altro/xdcc-go/internal/pubsub/pubsub.go — verificare pattern publish/close e rischio panic/race su channel chiusi.
- c:/progetti/altro/xdcc-go/internal/api/handlers_search.go — verificare waitgroup, handler WHOIS lifecycle, race su result channel, timeout behavior.
- c:/progetti/altro/xdcc-go/internal/sse/hub.go — verificare eviction client lenti, close semantics, publish safety.
- c:/progetti/altro/xdcc-go/internal/searchagg/aggregator.go — verificare timeout dei provider, cleanup cache concorrente, lock discipline, contention DB stats.
- c:/progetti/altro/xdcc-go/internal/searchagg/cache.go — verificare normalizzazione cache key, promozione cache concorrente, gestione errori JSON.
- c:/progetti/altro/xdcc-go/internal/store/sqlite.go — verificare parametri pool, metodi cancellabili e possibili colli di bottiglia.
- c:/progetti/altro/xdcc-go/cmd/xdcc-server/main.go — verificare shutdown order e timeout helper.
- c:/progetti/altro/xdcc-go/docs/plan_and_review/go_review_25_05_2026.md — riferimento di struttura e tono per il nuovo report.

**Verification**
1. Checklist di copertura: tutte le 6 aree richieste sono presenti con almeno un finding o nota esplicita di assenza criticità.
2. Checklist qualità finding: ogni finding contiene severità, file/linea, rischio, fix consigliato.
3. Controllo anti-false-positive: per i finding Alta severità, riconferma su sorgente con evidenza specifica del flusso concorrente.
4. Controllo finale formato: documento in docs/plan_and_review, nome richiesto, markdown leggibile, sezioni ordinate per priorità.

**Decisions**
- Inclusa solo analisi statica read-only, come richiesto.
- Esclusa qualsiasi validazione runtime (test/build/run) per vincolo esplicito.
- Prioritizzazione findings guidata da rischio operativo: panic/crash, leak persistenti, race con impatto su correttezza, poi smell e ottimizzazioni.

**Further Considerations**
1. Se desiderato, dopo la review si può produrre una seconda versione focalizzata solo su fix immediati (Top 5) con patch plan operativo.
2. Se desiderato, si può aggiungere una matrice rischio-probabilità-impatto per facilitare la pianificazione sprint.