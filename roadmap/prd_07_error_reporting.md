# PRD 07: Detaillierte Fehlerberichte (Detailed Error Reporting)

## 1. Einleitung & Ziel
Wenn eine Migration von Tausenden von Dateien durchgeführt wird, können einzelne Dateiübertragungen fehlschlagen (z. B. aufgrund von Netzwerk-Timeouts, Berechtigungsfehlern oder Quoten-Überschreitungen). Das Ziel dieses Features ist es, diese Fehler präzise zu erfassen, sie dem Benutzer im Frontend verständlich zu visualisieren und einen detaillierten Fehlerbericht (z. B. als CSV/JSON) für administrative Korrekturen bereitzustellen.

---

## 2. Anwendungsfälle (Use Cases)
*   **UC-1 (Fehlerdiagnose im UI):** Nach Abschluss einer Migration sieht der Benutzer im Dashboard: "995/1000 erfolgreich, 5 fehlgeschlagen". Er klickt auf ein Detail-Icon und sieht eine Liste der 5 fehlgeschlagenen Dateien mit dem genauen Grund (z. B. "Zielverzeichnis schreibgeschützt").
*   **UC-2 (Zentraler Export):** Ein IT-Administrator exportiert eine Excel-kompatible CSV-Datei der Fehlerliste, um die betroffenen Dateien auf dem Quell-Server manuell anzupassen oder Berechtigungen zu korrigieren.

---

## 3. Funktionale Anforderungen

| ID | Anforderung | Priorität | Beschreibung |
| :--- | :--- | :--- | :--- |
| **F-01** | Fehlerklassifizierung im Backend | MUST | Der Worker muss Fehler analysieren und in Kategorien einteilen (z. B. `AUTH_ERROR`, `TIMEOUT`, `STORAGE_FULL`, `FORBIDDEN_CHARACTERS`, `UNKNOWN`). |
| **F-02** | Fehler-Speicherung | MUST | Speichern des detaillierten Fehler-Stacktraces oder der API-Antwort im `error_message`-Feld der Tabelle `tasks`. |
| **F-03** | Interaktive Fehlerliste im UI | MUST | Ein neuer Tab "Fehlerbericht" im Migrations-Dashboard, der betroffene Dateien, Pfade, Größen und Fehlermeldungen tabellarisch auflistet. |
| **F-04** | CSV- und JSON-Export | MUST | Bereitstellung eines Download-Buttons im UI, um die Liste der fehlgeschlagenen Tasks als `.csv` oder `.json` herunterzuladen. |
| **F-05** | Selektiver Retry fehlgeschlagener Tasks | SHOULD | Möglichkeit, nur die fehlgeschlagenen Tasks einer Migration erneut anzustoßen, nachdem das Problem behoben wurde. |

---

## 4. Technische Schnittstellen & Architektur

### Datenbank & API-Endpunkte
*   **Datenbank:** Die Spalte `error_message` in der Tabelle `tasks` wird um strukturierte JSON-Fehler-Details erweitert oder enthält einen standardisierten Fehlercode.
*   **API-Endpunkt:** `GET /api/migration/{id}/errors`
    *   *Rückgabe:* Liste aller Tasks mit Status `FAILED`.

### Beispiel der API-Rückgabe (JSON)
```json
[
  {
    "id": "task-uuid-123",
    "file_path": "/Bilder/Urlaub.jpg",
    "file_size": 4512000,
    "error_code": "TIMEOUT",
    "error_message": "Post \"https://nextcloud.local/remote.php/dav/files/user/Bilder/Urlaub.jpg\": net/http: request canceled while waiting for connection (Client.Timeout exceeded while awaiting headers)"
  }
]
```

### CSV-Struktur
```csv
Dateipfad;Dateigröße (Bytes);Fehlercode;Details;Zeitpunkt
/Bilder/Urlaub.jpg;4512000;TIMEOUT;request canceled while waiting for connection;2026-07-09T20:05:00Z
```

---

## 5. Sicherheits- und Datenschutzaspekte (DSGVO)
*   **Datenminimierung:** Die Fehlermeldungen dürfen keine sensiblen Session-Tokens, Passwörter oder persönliche API-Keys im Klartext enthalten. Der Worker muss Fehlermeldungen vor dem Schreiben in die DB bereinigen (Sanitization).
*   **Löschfristen:** Mit dem Löschen der Migrationsdaten nach 24 Stunden werden auch alle Fehlerberichte rückstandslos vernichtet.

---

## 6. Akzeptanzkriterien
1. Schlägt ein Transfer wegen eines Timeouts fehl, wird der genaue HTTP-Fehler im Task protokolliert.
2. Im Dashboard wird bei Fehlern ein Tab "Fehlgeschlagene Dateien ({Anzahl})" eingeblendet.
3. Der CSV-Export generiert eine valide, Semikolon-separierte UTF-8 Datei mit allen fehlgeschlagenen Tasks.
4. Beim Klick auf "Retry Failed" werden nur die fehlerhaften Tasks wieder in den Status `PENDING` versetzt und neu in Redis eingereiht.
