# PRD 04: Inkrementelle Migration (Delta Sync)

## 1. Einleitung & Ziel
Das Ziel der inkrementellen Migration (Delta Sync) ist es, bei einem erneuten Ausführen einer bereits bestehenden Migration nur geänderte oder neue Dateien zu übertragen. Dies reduziert die benötigte Bandbreite, schont die Systemressourcen der beteiligten Server und minimiert das Ausfallfenster (Downtime) beim finalen Wechsel des Cloud-Dienstes.

---

## 2. Anwendungsfälle (Use Cases)
*   **UC-1 (Erstmigration & Nachsynchronisation):** Ein Benutzer migriert 500 GB Daten von Nextcloud zu Google Drive. Die Migration dauert 12 Stunden. Nach einigen Tagen möchte der Benutzer die Migration aktualisieren. Es sollen nur die in der Zwischenzeit geänderten oder neu hinzugefügten Dateien übertragen werden.
*   **UC-2 (Fehlerbehebung):** Eine Migration bricht nach 80% ab. Beim Neustart sollen die bereits erfolgreich übertragenen Dateien übersprungen werden.

---

## 3. Funktionale Anforderungen

| ID | Anforderung | Priorität | Beschreibung |
| :--- | :--- | :--- | :--- |
| **F-01** | Delta-Erkennungs-Modi | MUST | Das System muss drei Erkennungsmodi unterstützen:<br>1. **Größe & Änderungsdatum (schnell):** Vergleich der Dateigröße und des Modifikations-Zeitstempels (Last Modified).<br>2. **Prüfsummen-Vergleich (sicher):** Vergleich der kryptografischen Hashes (sofern vom Provider unterstützt).<br>3. **Kombiniert:** Nutzt Hashes als primären Check, fällt auf Datum/Größe zurück. |
| **F-02** | Task-Status "SKIPPED_EQUAL" | MUST | Dateien, die auf Quelle und Ziel identisch sind, werden im Indexierungsschritt als `SKIPPED` markiert und nicht in die Redis-Queue eingereiht. |
| **F-03** | Konfliktbehandlung bei Delta | MUST | Wenn eine Datei auf dem Ziel existiert, aber neuer oder anders ist als auf der Quelle, greift die eingestellte Konfliktstrategie (`OVERWRITE`, `RENAME`, `SKIP`). |
| **F-04** | Datenbank-Verlauf | SHOULD | Speichern des Verlaufs früherer Migrationsläufe, um Delta-Vergleiche auf DB-Ebene zu beschleunigen. |

---

## 4. Technische Schnittstellen & Architektur

### Ablauf des Delta-Scans (Indexierungsphase)
1. Das API-Gateway liest die Quellpfade aus und ruft parallel dazu die Zieldateien auf demselben relativen Pfad ab (mittels `GetDirectoryListing` oder `InspectResource`).
2. Für jedes Element wird ein Vergleich durchgeführt:
```go
func IsFileEqual(source, target storage.CloudResource, mode string) bool {
    if source.Size != target.Size {
        return false
    }
    if mode == "hash" && source.Hash != "" && target.Hash != "" {
        return source.Hash == target.Hash
    }
    // Toleranzbereich von 2 Sekunden bei Zeitstempeln (wegen FS-Unterschieden)
    timeDiff := source.LastModified.Sub(target.LastModified)
    if timeDiff < 0 {
        timeDiff = -timeDiff
    }
    return timeDiff <= 2*time.Second
}
```
3. Wenn `IsFileEqual == true`, wird der Task in der DB direkt mit dem Status `SKIPPED` angelegt, die Fortschritts-Statistik wird inkrementiert, aber der Task wird **nicht** in die Redis-Queue geschrieben.

---

## 5. Sicherheits- und Datenschutzaspekte (DSGVO)
*   Es werden weiterhin keine Dateiinhalte in der DB gespeichert, sondern nur die Metadaten (Pfade, Hashes, Größen).
*   Das Prinzip der Zero Data Retention bleibt gewahrt.

---

## 6. Akzeptanzkriterien
1. Beim zweiten Start einer Migration mit unveränderten Quelldateien werden 100 % der Dateien übersprungen (`SKIPPED`) und die Migration schließt innerhalb weniger Sekunden erfolgreich ab.
2. Wenn eine Datei auf der Quelle geändert wurde, wird sie beim erneuten Lauf als `PENDING` indexiert, übertragen und auf dem Ziel überschrieben oder umbenannt (je nach Strategie).
3. Korrekte Berechnung des Fortschritts im Dashboard (z.B. "100 von 100 Dateien verarbeitet, 95 übersprungen, 5 übertragen").
