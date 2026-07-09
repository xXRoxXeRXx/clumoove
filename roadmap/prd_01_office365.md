# PRD 01: Microsoft Office 365 Integration (OneDrive, SharePoint, Kontakte, Kalender)

## 1. Einleitung & Ziel
Ziel ist die Erweiterung der Migrations-Plattform um die Anbindung von Microsoft Office 365 (M365) Diensten. Dadurch können Benutzer ihre Dateien (OneDrive & SharePoint), Kalendereinträge (Outlook) und Kontakte (Outlook People) verlustfrei und DSGVO-konform zu anderen Cloud-Speichern (z.B. Nextcloud oder Google Drive) migrieren oder von diesen importieren.

---

## 2. Anwendungsfälle (Use Cases)
*   **UC-1:** Migration aller persönlichen Dateien eines Benutzers aus OneDrive for Business zu einer selbstgehosteten Nextcloud-Instanz.
*   **UC-2:** Umzug von geteilten Team-Dateien aus Microsoft SharePoint Document Libraries in ein gemeinsames WebDAV-Verzeichnis.
*   **UC-3:** Portierung aller Geschäftskontakte und persönlichen Kalender aus Microsoft Outlook zu Nextcloud CalDAV/CardDAV.

---

## 3. Funktionale Anforderungen

| ID | Anforderung | Priorität | Beschreibung |
| :--- | :--- | :--- | :--- |
| **F-01** | OAuth2-Authentifizierung | MUST | Registrierung einer Azure AD App und Implementierung des OAuth2-Flows (Authorization Code Flow mit Refresh-Token) zur sicheren Verbindungsherstellung. |
| **F-02** | OneDrive-Datei-Migration | MUST | Auflistung und rekursiver Download/Upload von OneDrive-Dateien und Ordnern. |
| **F-03** | SharePoint-Support | SHOULD | Zugriff auf SharePoint Site Document Libraries (Auswahl von Sites und deren Bibliotheken). |
| **F-04** | Outlook-Kalender-Export | SHOULD | Abruf aller Kalenderereignisse über die Graph API und Konvertierung in das standardisierte `.ics`-Format (iCalendar) für WebDAV/CalDAV-Kompatibilität. |
| **F-05** | Outlook-Kontakte-Export | SHOULD | Abruf aller Kontakte über die Graph API und Konvertierung in das standardisierte `.vcf`-Format (vCard) für WebDAV/CardDAV-Kompatibilität. |

---

## 4. Technische Schnittstellen & Architektur

### Backend-Integration
*   **Schnittstelle:** Ein neuer Adapter `OneDriveProvider` implementiert das [StorageProvider-Interface](file:///c:/Users/meyer/Development/migration/backend/internal/storage/provider.go).
*   **API-Client:** Verwendung der offiziellen Microsoft Graph REST API unter Verwendung der Go-Bibliotheken oder direkter HTTP-Requests mit OAuth2-Client-Unterstützung.
*   **Endpunkte der Microsoft Graph API:**
    *   *OneDrive:* `GET /me/drive/root/children` und `GET /me/drive/items/{item-id}/content`
    *   *SharePoint:* `GET /sites/{site-id}/drives`
    *   *Kalender:* `GET /me/events` oder `GET /me/calendars` (Konvertierung der Event-Objekte zu VCALENDAR/iCal).
    *   *Kontakte:* `GET /me/contacts` (Konvertierung der Kontakt-Objekte zu VCARD).

### Frontend-Integration
*   Erweiterung des [ConnectForm](file:///c:/Users/meyer/Development/migration/frontend/src/components/ConnectForm.tsx) um eine Schaltfläche "Mit Office 365 verbinden". 
*   Öffnen eines OAuth-Popups und Rückgabe des Access- und Refresh-Tokens an das Backend via Message-Event.

---

## 5. Sicherheits- und Datenschutzaspekte (DSGVO)
*   **Zero-Data-Retention:** Übertragene Dateien und Datenströme fließen flüchtig durch den RAM-Speicher des Workers und werden zu keinem Zeitpunkt lokal zwischengespeichert.
*   **Token-Verschlüsselung:** OAuth2-Tokens werden verschlüsselt in PostgreSQL abgelegt (AES-GCM unter Verwendung des `ENCRYPTION_SECRET_KEY`) und nur im Worker bei Bedarf entschlüsselt.

---

## 6. Akzeptanzkriterien
1. Der Benutzer kann sich erfolgreich über OAuth2 mit Office 365 verbinden.
2. Der Ordnerbaum von OneDrive wird im [FileBrowser](file:///c:/Users/meyer/Development/migration/frontend/src/components/FileBrowser.tsx) korrekt geladen.
3. Kalender und Kontakte werden geladen, als `.ics` / `.vcf` formatiert und fehlerfrei zum Zielserver übertragen.
4. Die Validierung der Dateigröße und Hashes (SHA256 der Graph API vs. Ziel-Hash) ist erfolgreich.
