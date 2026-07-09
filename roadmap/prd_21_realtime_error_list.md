# PRD 21: Echtzeit-Fehlerliste (Real-time Error List)

## 1. Einleitung & Ziel
Bei großen Migrationen, die mehrere Stunden oder Tage laufen, ist es ineffizient, erst nach dem vollständigen Abschluss über Fehler informiert zu werden. 
Das Ziel dieses Features ist es, eine **Echtzeit-Fehlerliste** im Dashboard bereitzustellen. Tritt bei einem Worker-Thread während der Migration ein Fehler auf, wird dieser innerhalb von Millisekunden via WebSockets an das Frontend gepusht und dort in einem dedizierten Fehler-Log-Fenster live angezeigt. Der Benutzer kann sofort reagieren (z. B. den Job pausieren oder das Passwort anpassen).

---

## 2. Anwendungsfälle (Use Cases)
*   **UC-1 (Sofortige Reaktion auf Passwort-Änderung):** Mitten in einer Migration läuft das App-Passwort der Nextcloud ab. Der Worker wirft Fehler. Diese tauchen sofort rot leuchtend in der Echtzeit-Fehlerliste auf. Der Administrator pausiert die Migration umgehend, anstatt den Job stundenlang fehlschlagen zu lassen.
*   **UC-2 (Live-Tracking):** Ein IT-Support-Mitarbeiter beobachtet eine aktive Migration im Dashboard und sieht live, welche Pfade wegen falscher Sonderzeichen blockiert werden.

---

## 3. Funktionale Anforderungen

| ID | Anforderung | Priorität | Beschreibung |
| :--- | :--- | :--- | :--- |
| **F-01** | WebSocket-Push | MUST | Fehlerereignisse der Worker müssen unverzüglich via WebSockets an den verbundenen Client gepusht werden. |
| **F-02** | Live-Error-Widget | MUST | Ein interaktives Konsolen- oder Tabellen-Widget im Dashboard, das neu eingehende Fehler ohne Seiten-Refresh anzeigt. |
| **F-03** | Filtermöglichkeit | SHOULD | Echtzeit-Filterung der Fehlerliste nach Fehlercode (z. B. `TIMEOUT`, `FORBIDDEN`), Pfad oder Schweregrad. |
| **F-04** | Desktop-Benachrichtigung | COULD | Optionale in-Browser Push-Benachrichtigung (Browser Notification API) bei Fehlern, wenn das Tab im Hintergrund läuft. |

---

## 4. Technische Schnittstellen & Architektur

### WebSocket- & Pub/Sub-Pipeline
1. Ein Worker scheitert beim Transfer einer Datei im [Processor](file:///c:/Users/meyer/Development/migration/backend/internal/processor/processor.go).
2. Der Worker schreibt den Fehler in die DB (`tasks.status = 'FAILED'`) und schickt ein Event an Redis Pub/Sub (`channel: migration_events:{migration_id}`).
3. Das API-Gateway lauscht auf dem Redis-Channel.
4. Das API-Gateway empfängt das Event und sendet es über die aktive WebSocket-Verbindung an das React-Frontend.

```
[Worker: Task Failed]
         │
         ▼
[Publish to Redis: migration_events:123]
         │
         ▼
[Go API Gateway (Subscription)]
         │
         ▼ (WebSocket Write)
[React Frontend (Dashboard.tsx)] ──► Renders in Real-time Error List
```

### WebSocket-Event Payload (Beispiel)
```json
{
  "type": "task_failed",
  "data": {
    "task_id": "987-abc",
    "file_path": "/Daten/Archiv.zip",
    "error_code": "FORBIDDEN_CHARACTERS",
    "error_message": "Invalid character ':' on destination storage class",
    "timestamp": "2026-07-09T20:25:00Z"
  }
}
```

---

## 5. Sicherheits- und Datenschutzaspekte (DSGVO)
*   **Authentifizierung:** Vor dem Verbindungsaufbau zum WebSocket-Endpunkt muss die Session des Benutzers validiert werden (JWT-Token im Query-Parameter oder Cookie unter Verwendung des `JWT_SECRET_KEY`).
*   **Mandanten-Sicherung (Isolation):** Ein Benutzer darf über WebSockets nur Events abonnieren können, die zu einer `migration_id` gehören, deren Eigentümer er ist (`user_id` Abgleich in der DB).

---

## 6. Akzeptanzkriterien
1. Tritt ein Task-Fehler auf, erscheint der Eintrag innerhalb von weniger als 1 Sekunde im Live-Error-Widget des Frontends, ohne dass der Benutzer die Seite neu laden muss.
2. Das Schließen oder Neuladen des Browsers führt nicht zum Verlust bereits empfangener Fehler des aktuellen Laufs (diese werden beim Wiederaufbau der Verbindung aus der DB nachgeladen).
3. Es werden keine unautorisierten WebSocket-Verbindungen zugelassen.
4. Es werden keine sensiblen Credentials oder Tokens im WebSocket-Payload übertragen.
