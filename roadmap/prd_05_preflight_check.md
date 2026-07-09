# PRD 05: Pre-Flight Check (Dry Run)

## 1. Einleitung & Ziel
Der Pre-Flight Check (Dry Run) ist eine vorbereitende Validierungsphase vor dem eigentlichen Start einer Migration. Er analysiert die Quell- und Zielstrukturen, um potenzielle Fehlerquellen (wie fehlende Berechtigungen, Speicherplatzmangel, inkompatible Dateinamen oder zu lange Pfade) zu erkennen und dem Benutzer zu melden, bevor zeit- und ressourcenaufwendige Datentransfers gestartet werden.

---

## 2. Anwendungsfälle (Use Cases)
*   **UC-1 (Speicherplatzprüfung):** Ein Benutzer plant eine Migration von 120 GB Daten. Das Ziel-Laufwerk hat jedoch nur noch 80 GB freien Speicher. Der Pre-Flight Check warnt den Benutzer und blockiert den Start, um einen Abbruch mittendrin zu verhindern.
*   **UC-2 (Namenskonflikte aufdecken):** Eine Quelle enthält Dateien mit dem Zeichen `:` (z.B. unter Linux/macOS). Das Ziel ist jedoch ein Windows-basiertes SMB-Laufwerk, welches `:` nicht im Dateinamen erlaubt. Der Check listet diese Dateien als "Warnungen" auf.

---

## 3. Funktionale Anforderungen

| ID | Anforderung | Priorität | Beschreibung |
| :--- | :--- | :--- | :--- |
| **F-01** | Verbindungs- und Schreibtest | MUST | Testet nicht nur die Verbindung (Connect), sondern versucht eine temporäre kleine Datei im Zielordner zu erstellen und wieder zu löschen (Schreib- & Löschberechtigung). |
| **F-02** | Quota-Check | MUST | Abruf des verfügbaren Speicherplatzes (Quota) des Ziels und Abgleich mit der Gesamtsumme der zu migrierenden Quelldateien. |
| **F-03** | Dateinamen- und Pfad-Validierung | MUST | Prüfung auf verbotene Zeichen (z.B. `\`, `/`, `:`, `*`, `?`, `"`, `<`, `>`, `|`) im Ziel-Dateisystem sowie Prüfung auf Pfadlängenüberschreitung (z.B. 255 Zeichen). |
| **F-04** | Warnungs-Dashboard | MUST | Anzeige der Ergebnisse im Frontend mit Kategorisierung (Error = blockiert Start, Warning = Info für Benutzer). |
| **F-05** | Option "Trotz Warnungen starten" | SHOULD | Erlaubt dem Benutzer, die Migration trotz unkritischer Warnungen (z.B. Pfadlängen) fortzusetzen. |

---

## 4. Technische Schnittstellen & Architektur

### Ablauf des Pre-Flight Checks
```
[Frontend: Klick auf "Pre-Flight Check"] 
       │
       ▼
[Backend: Endpoint /api/migration/preflight]
       │
       ├─► 1. Target-Schreibtest (Write/Delete Temporary File)
       ├─► 2. Quota-Abruf (Quelle Gesamtgröße vs. Ziel Freier Speicher)
       ├─► 3. Dateinamens-Scan (Regex-Prüfung auf verbotene Zeichen)
       │
       ▼
[Backend: Rückgabe JSON-Report mit Errors & Warnings]
       │
       ▼
[Frontend: Anzeige der Warnungs-Tabelle & Freigabe des Start-Buttons]
```

### API-Antwort Struktur (Beispiel)
```json
{
  "success": false,
  "errors": [
    {
      "code": "INSUFFICIENT_STORAGE",
      "message": "Nicht genügend Speicherplatz auf dem Zielsystem. Erforderlich: 120GB, Verfügbar: 80GB"
    }
  ],
  "warnings": [
    {
      "code": "INVALID_CHARACTERS",
      "path": "/Dokumente/Rechnung:2026.pdf",
      "message": "Das Zeichen ':' ist im Zielsystem nicht erlaubt und wird beim Transfer ersetzt oder führt zu Fehlern."
    }
  ]
}
```

---

## 5. Sicherheits- und Datenschutzaspekte (DSGVO)
*   Der Check liest nur Metadaten (Namen, Größen) und führt keine Leseoperationen auf den tatsächlichen Dateiinhalten durch.
*   Temporäre Testdateien für den Schreibtest werden sofort wieder gelöscht.

---

## 6. Akzeptanzkriterien
1. Wenn der Speicherplatz auf dem Ziel kleiner ist als die Größe der indexierten Quelldateien, schlägt der Check fehl und die Migration kann nicht gestartet werden.
2. Wenn der Ziel-Pfad schreibgeschützt ist, schlägt der Schreibtest fehl und zeigt eine detaillierte Fehlermeldung an.
3. Warnungen zu Dateinamen werden im UI tabellarisch dargestellt und der Benutzer kann entscheiden, ob er diese überspringen oder migrieren möchte.
