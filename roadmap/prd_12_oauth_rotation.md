# PRD 12: OAuth Key-Rotation-Daemon (Token Refresh Service)

## 1. Einleitung & Ziel
Bei der Anbindung von Drittanbieter-Clouds über OAuth2 (z. B. Google Drive, Dropbox, Microsoft Office 365) verfallen die Access-Tokens in der Regel nach einer Stunde. Um sicherzustellen, dass geplante (zeitgesteuerte) Migrationen oder langlaufende Transfers im Hintergrund nicht aufgrund abgelaufener Anmeldesitzungen fehlschlagen, ist ein autonomer Hintergrunddienst erforderlich. 

Der **OAuth Key-Rotation-Daemon** überwacht die hinterlegten OAuth-Credentials und erneuert die Access-Tokens mittels Refresh-Token rechtzeitig vor dem Ablauf.

---

## 2. Anwendungsfälle (Use Cases)
*   **UC-1 (Langzeitsynchronisation):** Ein Benutzer hat eine wöchentliche Migration von Google Drive zu Nextcloud eingerichtet. Der Job wird nachts um 03:00 Uhr ausgeführt. Der Daemon sorgt dafür, dass die OAuth2-Tokens rechtzeitig vor dem Start der Migration gültig sind, ohne dass eine Benutzerinteraktion im Browser erforderlich ist.
*   **UC-2 (Langlaufende Transfers):** Eine Migration sehr großer Dateien (z. B. 500 GB) dauert länger als die Gültigkeit des Access-Tokens (typischerweise 3600 Sekunden). Der Worker oder der Daemon erneuert das Token im laufenden Betrieb.

---

## 3. Funktionale Anforderungen

| ID | Anforderung | Priorität | Beschreibung |
| :--- | :--- | :--- | :--- |
| **F-01** | Proaktive Überwachung | MUST | Der Daemon scannt die Datenbank periodisch (z. B. alle 5 Minuten) nach OAuth-Verbindungen, deren Access-Token in weniger als 15 Minuten abläuft. |
| **F-02** | Token-Aktualisierung | MUST | Automatisches Senden des Refresh-Requests an den jeweiligen OAuth2-Provider (Google, Dropbox, Microsoft). |
| **F-03** | **Sofortige Entwertung (Token Rotation)** | MUST | Bei jedem Refresh-Request muss das alte Refresh-Token sofort als ungültig markiert/überschrieben werden, sobald das neue Token-Paar generiert und erfolgreich in der DB gespeichert wurde. Dies verhindert Replay-Angriffe. |
| **F-04** | Sichere Verwahrung | MUST | Verschlüsseltes Speichern und Lesen der Tokens. Niemals Weitergabe im Klartext an Hintergrund-Prozesse. |
| **F-05** | Fehlerbehandlung & Deaktivierung | SHOULD | Schlägt die Rotation mehrfach fehl (z. B. weil der Benutzer die App-Berechtigung im Google-Konto entzogen hat), wird die Verbindung als `EXPIRED` markiert und eine Benachrichtigung ausgelöst. |

---

## 4. Technische Schnittstellen & Architektur

### Ablauf der Token-Rotation
```
[Daemon Ticker: alle 5 Min]
         │
         ▼
[Suche in DB: expires_at < Now + 15 Min]
         │
         ▼
[Für jede betroffene Verbindung:]
         │
         ├─► 1. Entschlüssele altes Refresh-Token mit ENCRYPTION_SECRET_KEY
         ├─► 2. Sende POST-Request an Provider OAuth-Token-Endpunkt
         ├─► 3. Empfange neues Access- & Refresh-Token
         ├─► 4. Invaldiere altes Refresh-Token (überschreiben)
         ├─► 5. Verschlüssele neue Tokens und speichere in DB
         │
         ▼
[Protokollierung im Audit Log]
```

### Datenbank-Erweiterung (`oauth_connections`)
Speichern der OAuth-Metadaten für Quell- und Ziel-Verbindungen:

```sql
CREATE TABLE oauth_connections (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id UUID NOT NULL,                       -- Enforce Ownership Validation
    provider TEXT NOT NULL,                      -- 'google', 'dropbox', 'office365'
    access_token_encrypted TEXT NOT NULL,
    refresh_token_encrypted TEXT NOT NULL,
    expires_at TIMESTAMP WITH TIME ZONE NOT NULL,
    updated_at TIMESTAMP WITH TIME ZONE DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX idx_oauth_expires ON oauth_connections(expires_at);
```

---

## 5. Sicherheits- und Datenschutzaspekte (DSGVO)

> [!IMPORTANT]
> **Strikte Einhaltung der Sicherheitsrichtlinien:**
> *   **Key Segregation:** Der Schlüssel `ENCRYPTION_SECRET_KEY` wird ausschließlich für die AES-256-GCM Ver- und Entschlüsselung der Credentials/Tokens in der Datenbank verwendet. Der `JWT_SECRET_KEY` wird streng separat nur für JWT-Token-Signaturen verwendet.
> *   **Zero Plaintext in Goroutines:** Tokens dürfen nicht im Klartext im Speicher gehalten oder an asynchrone Goroutines übergeben werden. Die Entschlüsselung mittels `crypto.Decrypt` erfolgt erst unmittelbar vor dem HTTP-Request an den OAuth-Provider.
> *   **Token Rotation Constraint:** Bei jedem Refresh-Vorgang muss das alte Refresh-Token sofort durch das neu gelieferte Refresh-Token überschrieben werden.

---

## 6. Akzeptanzkriterien
1. Der Daemon läuft als Hintergrund-Goroutine innerhalb des API-Gateways und führt alle 5 Minuten einen Scan durch.
2. Wenn ein Access-Token in 10 Minuten abläuft, führt der Daemon erfolgreich den Refresh-Flow aus, aktualisiert `access_token_encrypted`, `refresh_token_encrypted` sowie `expires_at` in der DB und verwirft das alte Token.
3. Im Falle eines Fehlers (z. B. HTTP 400 Bad Request vom Provider) wird der Status auf `INVALID` gesetzt, sodass keine weiteren automatischen Zugriffe versucht werden, um Rate-Limits nicht zu verletzen.
4. Alle Entschlüsselungsvorgänge nutzen die AES-GCM-Logik mit dem dedizierten `ENCRYPTION_SECRET_KEY`.
