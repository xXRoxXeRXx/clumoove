# PRD 17: Zwei-Faktor-Authentisierung (2FA / MFA)

## 1. Einleitung & Ziel
Die Erhöhung der Plattformsicherheit ist essenziell, da die Migrations-Plattform Zugriff auf sensible Cloud-Speicher hat. Dieses PRD definiert zwei Bereiche für die Zwei-Faktor-Authentisierung (2FA):
1.  **Plattform-Ebene (MFA):** Absicherung des Benutzer-Logins im Migrations-Dashboard mittels TOTP (Time-based One-Time Password, z.B. Google Authenticator).
2.  **Provider-Ebene:** Umgang mit 2FA auf Quell- und Ziel-Systemen (z. B. OAuth2-Flows oder Erzwingen von App-Passwörtern für Nextcloud/WebDAV).

---

## 2. Anwendungsfälle (Use Cases)
*   **UC-1 (Dashboard-Login):** Ein Benutzer loggt sich in die Migrations-Plattform ein. Er gibt E-Mail und Passwort ein. Danach wird er nach seinem 6-stelligen Authenticator-Code gefragt, um den Zugriff freizugeben.
*   **UC-2 (2FA bei Nextcloud):** Ein Benutzer möchte seine Nextcloud verbinden. Da er dort 2FA aktiv hat, scheitert der direkte Login. Das UI erkennt dies oder weist den Benutzer an, ein "App-Passwort" in seinen Nextcloud-Einstellungen zu erstellen.
*   **UC-3 (2FA bei Google/Dropbox):** Der integrierte OAuth2-Flow leitet den Benutzer auf die offizielle Google-Login-Seite weiter. Google wickelt die 2FA (z. B. SMS-Code oder App-Push) eigenständig ab und gibt das OAuth2-Token an uns zurück.

---

## 3. Funktionale Anforderungen

| ID | Anforderung | Priorität | Beschreibung |
| :--- | :--- | :--- | :--- |
| **F-01** | TOTP 2FA Aktivierung | MUST | Benutzer können in ihren Profileinstellungen einen 2FA-QR-Code scannen und 2FA für ihr Plattform-Konto aktivieren. |
| **F-02** | TOTP Verifizierung | MUST | Login-Schnittstelle verlangt nach Aktivierung bei jedem Login den OTP-Token. Validierung im Go-Backend. |
| **F-03** | Wiederherstellungscodes (Backup Keys) | MUST | Generierung von 8-stelligen Backup-Codes bei der 2FA-Einrichtung für den Fall, dass der Benutzer sein Smartphone verliert. |
| **F-04** | WebDAV 2FA Guidance | MUST | Erkennung von 2FA-bedingten Login-Fehlern (`401 Unauthorized` trotz richtiger Zugangsdaten) bei WebDAV/Nextcloud. Anzeige einer klaren Anleitung zur Erstellung eines App-Passworts. |
| **F-05** | OAuth2-Redirection | MUST | Vollständige Delegation der 2FA-Abwicklung an die Identity-Provider von Google, Microsoft und Dropbox über OAuth2-Standard-Redirections. |

---

## 4. Technische Schnittstellen & Architektur

### TOTP-Implementierung im Go-Backend
*   **Bibliothek:** Verwendung von `github.com/pquerna/otp/totp` zur Generierung von QR-Codes und zur Validierung der 6-stelligen OTP-Tokens.
*   **Ablauf bei Login:**
    1. Benutzer sendet E-Mail und Passwort an `/api/auth/login`.
    2. Ist 2FA aktiv, antwortet die API mit `202 Accepted` und einer temporären Session-ID.
    3. Das Frontend zeigt ein OTP-Eingabefeld.
    4. Benutzer sendet OTP-Code an `/api/auth/totp`.
    5. API prüft den Code mit `totp.Validate`. Ist er korrekt, wird das JWT-Token signiert (unter Verwendung des `JWT_SECRET_KEY`) und an den Benutzer übergeben.

### Nextcloud App-Passwort Erkennung
Schlägt `Connect` im [NextcloudProvider](file:///c:/Users/meyer/Development/migration/backend/internal/storage/nextcloud.go) fehl und deutet die Serverkonfiguration auf aktives 2FA hin (z. B. über Header oder HTTP 401), gibt das Backend einen spezifischen Fehlercode an das Frontend zurück:

```json
{
  "status": "error",
  "code": "MFA_REQUIRED_ON_PROVIDER",
  "message": "Dieser Nextcloud-Account ist durch Zwei-Faktor-Authentisierung geschützt. Bitte erstelle in deinen Nextcloud-Einstellungen unter 'Sicherheit' ein App-Passwort und verwende dieses anstelle deines Hauptpassworts."
}
```

---

## 5. Sicherheits- und Datenschutzaspekte (DSGVO)
*   **Key Segregation:** TOTP-Secrets werden verschlüsselt in PostgreSQL abgelegt (AES-GCM unter Verwendung des `ENCRYPTION_SECRET_KEY`).
*   **Brute-Force Schutz:** Sperrung des 2FA-Eingabefeldes nach 5 Fehleingaben für 15 Minuten.

---

## 6. Akzeptanzkriterien
1. Der Benutzer kann 2FA über einen QR-Code einrichten. Die Einrichtung wird erst abgeschlossen, wenn der Benutzer den ersten generierten Code erfolgreich verifiziert hat.
2. Nach der Aktivierung ist ein Einloggen ohne den 6-stelligen TOTP-Code unmöglich.
3. OAuth2-Verbindungen zu Google und Dropbox funktionieren reibungslos, auch wenn der Benutzer dort 2FA/MFA aktiv hat.
4. Fehlgeschlagene Logins bei Nextcloud wegen 2FA zeigen die dedizierte Warnmeldung mit der Anleitung für App-Passwörter an.
