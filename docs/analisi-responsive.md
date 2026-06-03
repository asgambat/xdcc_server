# Analisi Responsive per Dispositivi Mobili (5-6 pollici)

## 1. Stato Attuale
Il frontend utilizza un layout a due colonne (sidebar + contenuto) con una media query a 768px. Su schermi piccoli (5-6 pollici), la gestione del layout soffre di:
- Tabelle che forzano l'orizzontalità.
- Griglie di statistiche che non si adattano al meglio a larghezze < 350px.
- Spaziature ottimizzate per mouse e non per dita (tap targets).

## 2. Interventi per Task

### Task 1: Ottimizzazione Sidebar e Header
- **Obiettivo:** Migliorare l'accessibilità della navigazione su schermi piccoli.
- **Azione:** Ridurre il padding della mobile-header e aumentare la dimensione del pulsante hamburger. Rendere la sidebar sovrapposta (overlay) a tutto schermo quando aperta.

### Task 2: Refactoring delle Tabelle (DownloadTable)
- **Obiettivo:** Eliminare l'overflow orizzontale.
- **Azione:** Implementare un pattern "Responsive Table". Per schermi < 600px, convertire ogni riga della tabella in una scheda (card) verticale con etichette visibili.

### Task 3: Griglie Adattive (Stats e Form)
- **Obiettivo:** Ottimizzare lo spazio verticale.
- **Azione:** Modificare `.stats-grid` in `web/src/app.css` impostando `minmax(150px, 1fr)` per garantire che gli elementi si impilino correttamente anche su larghezze ridotte.

### Task 4: Tap Targets e Spaziature
- **Obiettivo:** Migliorare l'usabilità touch.
- **Azione:** Aumentare la `min-height` di tutti i pulsanti (.btn) a 44px e incrementare il touch target per gli elementi interattivi nelle liste.

### Task 5: Modal e Overlay
- **Obiettivo:** Migliorare la gestione modale su mobile.
- **Azione:** Assicurare che `.modal` in `app.css` occupi il 95% della larghezza su schermi piccoli, migliorando la leggibilità del contenuto.

## 3. Roadmap Prioritaria
1. Refactoring Tabelle (Priorità Alta)
2. Ottimizzazione Griglie e Spaziature (Priorità Media)
3. Raffinamento Sidebar/Mobile Header (Priorità Media)
4. Test e Validazione Mobile (Priorità Alta)
