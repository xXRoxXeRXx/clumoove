# PRD 10: Pausieren und Abbrechen einer aktiven Migration

## 1. Einleitung & Ziel
Dieses Dokument spezifiziert die Anforderungen für die manuelle Steuerung aktiver Migrationen. Benutzer müssen die Möglichkeit haben, einen laufenden Migrationsprozess jederzeit zu pausieren (z. B. bei hoher Systemauslastung) und später fortzusetzen oder die Migration vollständig und sauber abzubrechen. Das System muss sicherstellen, dass hierbei keine Daten korrumpiert werden und Ressourcen ordnungsgemäß freigegeben werden.

---

## 2. Anwendungsfälle (Use Cases)
*   **UC-1 (Migration Pausieren & Fortsetzen):** Ein Benutzer stellt fest, dass sein lokaler Webserver durch die Migration überlastet ist. Er klickt im Dashboard auf "Pausieren". Der Worker stoppt den aktuellen Transfer nach Fertigstellung des laufenden Blocks. Später klickt der Benutzer auf "Fortsetzen" (Resume), woraufhin der Transfer am Pausierungspunkt fortgesetzt wird.
*   **UC-2 (Migration Abbrechen):** Ein Benutzer hat falsche Pfade ausgewählt. Er klickt auf "Abbrechen". Alle noch ausstehenden Tasks in der Queue werden verworfen, aktive Streams werden abgebrochen und die Migration wechselt in den Endzustand `CANCELLED`.

---

## 3. Funktionale Anforderungen

| ID | Anforderung | Priorität | Beschreibung |
| :--- | :--- | :--- | :--- |
| **F-01** | Pause-Funktion (Graceful Pause) | MUST | Stoppt das Dequeuing neuer Tasks. Bereits aktive Datei-Transfers werden sauber zu Ende geschrieben, bevor der Worker in den Ruhezustand übergeht. |
| **F-02** | Fortsetzen (Resume) | MUST | Setzt den Status der Migration wieder auf `RUNNING` und signalisiert den Workern, wieder Tasks aus der Redis-Queue zu holen. |
| **F-03** | Abbruch-Funktion (Hard Cancel) | MUST | Bricht aktive Datei-Streams sofort über Context-Cancellation ab, löscht unvollständige temporäre Dateien auf dem Zielserver und leert die verbleibende Queue in Redis. |
| **F-04** | Live-UI-Buttons | MUST | Anzeige von reaktiven Steuerungselementen (Pause, Resume, Cancel) im Dashboard basierend auf dem aktuellen Migrationsstatus. |

---

## 4. Technische Schnittstellen & Architektur

### Statusübergänge (State Machine)
```
          ┌──────────────┐
          │   PENDING    │
          └──────┬───────┘
                 │ Start
                 ▼
          ┌──────────────┐
   ┌─────►│   RUNNING    ├──────┐
   │      └──────┬───────┘      │
   │ Resume      │ Pause        │ Cancel
   │             ▼              ▼
   │      ┌──────────────┐  ┌──────────────┐
   └──────┤    PAUSED    │  │  CANCELLED   │
          └──────────────┘  └──────────────┘
```

### Koordination über Redis & Context-Cancellation
*   **Pause:**
    1. Der Benutzer klickt auf "Pause" -> REST-API setzt den Status in PostgreSQL auf `PAUSED`.
    2. Das API-Gateway sendet ein Event über Redis Pub/Sub (`migration-control:pause:{migration_id}`).
    3. Worker empfangen das Signal. Die Hauptschleife holt keine neuen Tasks mehr ab. Der aktuell laufende Task wird normal beendet.
*   **Cancel (Abbruch):**
    1. Der Benutzer klickt auf "Cancel" -> REST-API setzt den Status auf `CANCELLED`.
    2. Die Redis-Queue für diese Migration wird gelöscht (Verwerfen aller ausstehenden Tasks).
    3. Ein Abbruchsignal über Redis Pub/Sub triggert die Context-Cancellation (`context.CancelFunc`) im Worker für den aktuell laufenden Task.
    4. Der Worker bricht den HTTP-Stream sofort ab.
    5. Falls `deleteAfterUpload` oder ein temporärer Pfad aktiv war, führt der Worker einen Bereinigungs-Request (`DeleteFile`) auf dem Zielserver aus, um Datenfragmente (.tmp-Dateien) zu löschen.

---

## 5. Sicherheits- und Datenschutzaspekte (DSGVO)
*   **Aufräum-Garantie:** Bei einem Abbruch (Cancel) sorgt das System dafür, dass keine halben oder ungültigen Dateien auf dem Zielserver zurückbleiben, die später manuell gelöscht werden müssten.

---

## 6. Akzeptanzkriterien
1. Klickt der Benutzer auf "Pause", pausiert der Fortschritt im Frontend. Nach Fertigstellung des aktiven Dateitransfers werden keine weiteren Dateien mehr übertragen.
2. Nach Klick auf "Resume" wird die Migration nahtlos fortgesetzt, ohne dass bereits übertragene Dateien erneut gesendet werden.
3. Klickt der Benutzer auf "Cancel", bricht ein aktiver Transfer (z. B. einer 5 GB Datei) innerhalb von maximal 2 Sekunden ab.
4. Alle abgebrochenen Tasks hinterlassen keine verwaisten temporären Dateien (`.tmp`) auf dem Zielsystem.
5. Die verbleibenden Tasks der abgebrochenen Migration werden aus Redis entfernt.
