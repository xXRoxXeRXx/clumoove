# PRD 03: SMB/CIFS Network Share Integration

## 1. Einleitung & Ziel
Dieses Dokument beschreibt die funktionalen und technischen Anforderungen zur Anbindung von Windows-Netzwerkfreigaben (SMB/CIFS) an die Migrations-Plattform. Dies ermöglicht Unternehmen und Heimanwendern, lokale Netzwerkpfade (z.B. von Synology, QNAP oder Windows Servern) direkt als Quelle oder Ziel für Migrationen zu nutzen.

---

## 2. Anwendungsfälle (Use Cases)
*   **UC-1:** Migration eines lokalen SMB-Freigabeordners (`\\nas\projekte`) in eine Nextcloud-Cloud-Instanz.
*   **UC-2:** Backup von Nextcloud-Daten direkt auf eine passwortgeschützte SMB-Freigabe im lokalen Netzwerk.

---

## 3. Funktionale Anforderungen

| ID | Anforderung | Priorität | Beschreibung |
| :--- | :--- | :--- | :--- |
| **F-01** | SMB-Verbindungsaufbau | MUST | Eingabe von Hostname/IP, Port (Standard: 445), Freigabename (Share), Username, Passwort und optionaler Domain. |
| **F-02** | Verzeichnisstruktur auflisten | MUST | Rekursives Auslesen der SMB-Verzeichnisstruktur zur Indexierung in der PostgreSQL-Datenbank. |
| **F-03** | Streamed I/O | MUST | Direktes Streamen beim Schreiben (Upload) und Lesen (Download) von Dateien ohne lokalen Platten-Cache auf dem Worker. |
| **F-04** | SMB v2/v3 Support | MUST | Unterstützung moderner und sicherer SMB-Protokolle (SMBv2 und SMBv3). SMBv1 (unsicher) sollte explizit abgelehnt werden. |
| **F-05** | Dateieigenschaften & Rechte | SHOULD | Erhalt von Erstellungs- und Änderungsdaten der Dateien während der Migration. |

---

## 4. Technische Schnittstellen & Architektur

### Backend-Integration
*   **Schnittstelle:** Ein neuer Adapter `SMBProvider` implementiert das [StorageProvider-Interface](file:///c:/Users/meyer/Development/migration/backend/internal/storage/provider.go).
*   **Bibliothek:** Verwendung einer nativen Go-SMB-Bibliothek (z.B. `github.com/hirochachacha/go-smb2`), da diese keine externen C-Dependencies (wie libsmbclient) benötigt und somit plattformunabhängig und leicht im Docker-Container zu betreiben ist.
*   **Ablauf:**
    *   Aufbau einer TCP-Verbindung zu Port 445 des Zielsystems.
    *   Authentifizierung und Erstellung einer SMB-Session.
    *   Mounten des Shares im Speicher.
    *   Verwendung von `Session.Mount(shareName)` und `Share.ReadDir` / `Share.Open`.

### Frontend-Integration
*   Erweiterung des [ConnectForm](file:///c:/Users/meyer/Development/migration/frontend/src/components/ConnectForm.tsx) um den Typ "SMB/CIFS Freigabe".
*   Felder: Server IP/Host, Port, Share Name, Domain (optional), Username, Passwort.

---

## 5. Sicherheits- und Datenschutzaspekte (DSGVO)
*   **Sichere Authentifizierung:** Passwörter werden verschlüsselt gespeichert (`ENCRYPTION_SECRET_KEY`) und nur zur Laufzeit entschlüsselt.
*   **Protokollsicherheit:** Verbindung erzwingt Verschlüsselung (SMB3 Encryption), sofern vom Server unterstützt. SMBv1-Verbindungen werden aus Sicherheitsgründen blockiert.
*   **Zero-Caching:** Die Daten werden direkt über Speicher-Buffer (RAM) weitergeleitet.

---

## 6. Akzeptanzkriterien
1. Erfolgreicher Verbindungstest zu einer Windows-Freigabe und einer Samba-Freigabe (Synology NAS).
2. Vollständige, fehlerfreie Indexierung von Verzeichnissen mit Umlauten und Leerzeichen im Namen.
3. Erfolgreiche Migration von Dateien über SMBv2 und SMBv3 mit korrekter Fortschrittsanzeige im Dashboard.
