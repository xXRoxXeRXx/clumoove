# PRD 09: Benachrichtigungen (Webhooks & E-Mail)

## 1. Einleitung & Ziel
Dieses PRD beschreibt die Anforderungen an ein Benachrichtigungssystem, das den Benutzer automatisch über den Status seiner Migrations-Jobs informiert. Benutzer müssen nicht mehr permanent das Web-Interface geöffnet halten, sondern erhalten Push-Nachrichten oder E-Mails, wenn ein Job erfolgreich abgeschlossen wurde, fehlgeschlagen ist oder aufgrund eines Verbindungsausfalls pausiert wurde.

---

## 2. Anwendungsfälle (Use Cases)
*   **UC-1 (Slack/Teams Webhook):** Ein System-Administrator startet eine Server-Migration vor dem Feierabend. Er hinterlegt den Webhook-Link seines Team-Chats. Bei Fertigstellung oder bei Fehlern postet die Plattform eine formatierte Nachricht in den Chat.
*   **UC-2 (E-Mail bei Abschluss):** Ein Benutzer lässt seine privaten Nextcloud-Daten migrieren. Nach 4 Stunden erhält er eine E-Mail mit der Zusammenfassung: "Migration erfolgreich abgeschlossen. 45.210 Dateien übertragen (142 GB), 0 Fehler."

---

## 3. Funktionale Anforderungen

| ID | Anforderung | Priorität | Beschreibung |
| :--- | :--- | :--- | :--- |
| **F-01** | E-Mail-Versand (SMTP) | MUST | Versand von systemgenerierten E-Mails bei Statusänderungen (Erfolgreich, Fehlgeschlagen, Pausiert). |
| **F-02** | Slack & MS Teams Webhooks | MUST | Unterstützung ausgehender Webhooks im JSON-Format, optimiert für Slack ("Incoming Webhooks") und Microsoft Teams ("Office 365 Connectors"). |
| **F-03** | Konfigurierbare Ereignisse | MUST | Der Benutzer kann im Frontend wählen, bei welchen Events er benachrichtigt werden möchte (z. B. *Nur bei Fehlern* oder *Bei jedem Statuswechsel*). |
| **F-04** | E-Mail-Templates | SHOULD | HTML- und Text-basierte E-Mail-Vorlagen mit detaillierter Statistik (Dauer, Datenmenge, Fehlerrate). |
| **F-05** | Benutzerdefinierte Webhooks | COULD | Generische Webhooks mit konfigurierbarem JSON-Body und Header-Variablen für eigene API-Anbindungen. |

---

## 4. Technische Schnittstellen & Architektur

### Ereignisgesteuerte Notification-Pipeline
1. Der Status einer Migration ändert sich in der DB (z. B. auf `COMPLETED` oder `PAUSED_CONNECTION_LOSS`).
2. Das API-Gateway oder der Worker wirft ein internes Event in eine separate Redis-Queue für Benachrichtigungen (`migration-notifications`).
3. Ein asynchroner Notification-Worker liest die Queue aus, zieht sich die Konfiguration des Jobs (z. B. Webhook-URL, E-Mail-Empfänger) und sendet die Nachricht ab.

```
[Migration-State-Change] ──► [Redis Queue: notifications] ──► [Notification-Service]
                                                                   │
                                                                   ├──► SMTP (E-Mail)
                                                                   └──► HTTP POST (Slack / Teams)
```

### Konfigurationstabelle in PostgreSQL
```sql
CREATE TABLE notification_settings (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    migration_id UUID NOT NULL REFERENCES migrations(id) ON DELETE CASCADE,
    channel_type TEXT NOT NULL,          -- 'email', 'slack', 'teams', 'generic'
    destination TEXT NOT NULL,            -- E-Mail-Adresse oder Webhook-URL
    notify_on_success BOOLEAN DEFAULT TRUE,
    notify_on_failure BOOLEAN DEFAULT TRUE,
    notify_on_pause BOOLEAN DEFAULT TRUE
);
```

---

## 5. Sicherheits- und Datenschutzaspekte (DSGVO)
*   **Datenschutz:** In Webhook- payloads und E-Mails dürfen niemals Passwörter, kryptografische Schlüssel oder Access-Tokens übertragen werden.
*   **SMTP-Credentials:** Die SMTP-Zugangsdaten des System-Absenders werden sicher verschlüsselt in der `.env`-Konfiguration auf dem Server verwaltet.

---

## 6. Akzeptanzkriterien
1. Nach erfolgreicher Konfiguration eines Slack-Webhooks sendet das System eine Test-Nachricht an den Channel.
2. Schließt eine Migration erfolgreich ab, erhält der Benutzer eine E-Mail mit der Betreffzeile: `[CloudMove] Migration erfolgreich abgeschlossen (ID: ...)` und einer tabellarischen Auswertung im Body.
3. Netzwerk-Timeouts beim E-Mail-Versand oder beim Aufruf von Webhooks blockieren nicht den Migrations-Prozess des Workers (vollkommen asynchrone Abarbeitung).
