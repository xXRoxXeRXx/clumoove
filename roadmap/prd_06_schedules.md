# PRD 06: Core Scheduler Engine (Zentraler Taktgeber)

## 1. Einleitung & Ziel
Dieses Dokument beschreibt die Anforderungen zur Implementierung der zentralen **Core Scheduler Engine** im Backend. Das System benötigt eine einheitliche Infrastruktur, um zeitgesteuerte Aufgaben (Migrationen, Synchronisationen und Backups) zu verwalten. 

Anstatt dass jeder Dienst (Migration, Sync, Backup) seine eigene Zeitsteuerungs-Logik implementiert, stellt die Scheduler Engine einen zentralen Daemon bereit, der anstehende Aufgaben triggert und in die Redis-Queue einreiht.

---

## 2. Anwendungsfälle (Use Cases)
*   **UC-1 (Einmaliger verzögerter Start):** Ein Benutzer plant eine große One-Shot-Migration für 02:00 Uhr nachts ein. Der Scheduler startet die Migration einmalig zur Zielzeit und deaktiviert sich danach für diese Aufgabe.
*   **UC-2 (Wiederkehrende Dienstleistungen):** Ein Benutzer konfiguriert ein tägliches Backup (PRD 14) und eine stündliche Synchronisation (PRD 13). Der Scheduler triggert diese Aufgaben wiederkehrend basierend auf ihren Cron-Ausdrücken.

---

## 3. Funktionale Anforderungen

| ID | Anforderung | Priorität | Beschreibung |
| :--- | :--- | :--- | :--- |
| **F-01** | Generisches Scheduling | MUST | Der Scheduler muss verschiedene Task-Typen (`migration`, `sync`, `backup`) anhand einer einheitlichen Datenstruktur steuern können. |
| **F-02** | Scheduler-Intervalle | MUST | Unterstützung von:<br>1. **Einmalig verzögert:** Start zu einem festen Zeitpunkt (Timestamp).<br>2. **Wiederkehrend:** Start basierend auf einem Standard-Cron-Ausdruck (z. B. `*/30 * * * *` für alle 30 Minuten). |
| **F-03** | Scheduler-Daemon | MUST | Eine Hintergrund-Goroutine im API-Gateway prüft minütlich fällige Aufgaben und reiht sie in die Redis-Queue ein. |
| **F-04** | Überlappungsschutz | MUST | Vor dem Start eines wiederkehrenden Jobs (z.B. Sync) muss geprüft werden, ob die vorherige Instanz dieses Jobs noch aktiv ist (Status `RUNNING` oder `INDEXING`). Falls ja, wird der neue Lauf übersprungen und protokolliert, um Ressourcen-Konflikte zu vermeiden. |
| **F-05** | Lifecycle-Handling | MUST | - **One-Shot Tasks:** Nach erfolgreicher Ausführung wird der Zeitplan deaktiviert (`is_active = false`).<br>- **Recurring Tasks:** Nach dem Trigger wird `next_run_at` anhand des Cron-Ausdrucks neu berechnet. |

---

## 4. Technische Schnittstellen & Architektur

### Datenbank-Design (Zentrale Tabelle `schedules`)
```sql
CREATE TABLE schedules (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id UUID NOT NULL,                        -- Multi-Tenancy / Isolation
    task_type TEXT NOT NULL,                      -- 'migration', 'sync', 'backup'
    task_id UUID NOT NULL,                        -- ID aus migrations, sync_jobs, oder backup_jobs
    cron_expression TEXT,                         -- NULL bei einmalig geplanten Aufgaben
    run_at TIMESTAMP WITH TIME ZONE,              -- Festgelegte Startzeit für einmalige Aufgaben
    next_run_at TIMESTAMP WITH TIME ZONE,         -- Nächster errechneter Startzeitpunkt
    is_active BOOLEAN NOT NULL DEFAULT TRUE,
    created_at TIMESTAMP WITH TIME ZONE DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMP WITH TIME ZONE DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX idx_schedules_next_run ON schedules(next_run_at) WHERE is_active = TRUE;
```

### Backend-Architektur (Go)
*   **Bibliothek:** Verwendung von `github.com/robfig/cron/v3` zur Validierung und Berechnung von Cron-Ausdrücken (`cron.ParseStandard`).
*   **Verarbeitungsschleife (Ticker):**
    1. Der Daemon holt jede Minute alle Zeilen aus `schedules`, bei denen `is_active = true` und `next_run_at <= NOW()` gilt.
    2. Für jeden fälligen Eintrag wird geprüft, ob der verknüpfte Job (in `migrations`, `sync_jobs` oder `backup_jobs`) bereits aktiv ist.
    3. Ist er frei, wird ein Startsignal (Task-Payload) an die Redis-Queue gesendet.
    4. Aktualisierung der Tabelle `schedules`:
        *   Wenn `cron_expression` gesetzt ist: Berechne den nächsten Ausführungszeitpunkt und setze `next_run_at = nextTime`.
        *   Wenn `cron_expression` NULL ist (einmaliger Lauf): Setze `is_active = false`.

---

## 5. Sicherheits- und Datenschutzaspekte (DSGVO)
*   **Mandantentrennung (Multi-Tenancy):** Jede Abfrage oder Manipulation der `schedules` muss über die authentifizierte `user_id` aus dem Request-Kontext isoliert werden.
*   **Zugangsdaten-Sicherheit:** Der Scheduler liest selbst keine Passwörter. Er triggert nur die Ausführung. Die jeweiligen Engines (Sync, Backup, Migration) holen sich die Passwörter erst im Worker und entschlüsseln sie unmittelbar vor der Verbindung mittels `crypto.Decrypt`.

---

## 6. Akzeptanzkriterien
1. Ein geplanter One-Shot-Job wird exakt einmal ausgeführt; danach ist `is_active = false`.
2. Ein wiederkehrender Job berechnet nach jedem Start seinen nächsten Ausführungszeitpunkt korrekt und läuft dauerhaft weiter.
3. Wenn ein stündlicher Sync-Job 90 Minuten dauert, wird der Trigger nach 60 Minuten übersprungen (Überlappungsschutz aktiv).
4. Das Löschen eines Benutzers oder Jobs löscht kaskadierend alle zugehörigen Einträge in `schedules`.
