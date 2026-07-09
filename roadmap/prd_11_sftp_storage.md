# PRD 11: SFTP Storage Integration (Secure File Transfer Protocol)

## 1. Einleitung & Ziel
Dieses PRD spezifiziert die Integration von SFTP (SSH File Transfer Protocol) als Speicher-Provider in der Migrations-Plattform. Dadurch können Daten von klassischen Webservern, Linux-Servern oder entfernten Backupsystemen direkt in moderne Cloud-Strukturen migriert oder von dort bezogen werden.

---

## 2. Anwendungsfälle (Use Cases)
*   **UC-1 (Migration vom Webspace):** Ein Webmaster migriert Mediendateien (`/var/www/uploads`) eines alten Linux-Webservers direkt in ein Nextcloud-Verzeichnis.
*   **UC-2 (Zentrales Backup):** Ein Unternehmen sichert Dokumente aus Google Drive direkt auf einen verschlüsselten SFTP-Speicher im eigenen Rechenzentrum.

---

## 3. Funktionale Anforderungen

| ID | Anforderung | Priorität | Beschreibung |
| :--- | :--- | :--- | :--- |
| **F-01** | SFTP-Verbindungs-Setup | MUST | Eingabe von Host, Port (Standard: 22), Username, Passwort und optional einem SSH-Private-Key (RSA/ED25519) im Frontend sowie Validierung. |
| **F-02** | SSH-Schlüsselaustausch & Host-Key | MUST | Unterstützung sicherer SSH-Key-Authentifizierung. Optionaler Support für Host-Key-Verifizierung (Known Hosts). |
| **F-03** | Rekursive Dateilistung | MUST | Einlesen und hierarchische Strukturierung des entfernten Dateisystems zur Indexierung. |
| **F-04** | Streamed Download / Upload | MUST | Streamen der Daten über SSH-Kanäle direkt in die Engine (keine lokale Dateizwischenspeicherung). |
| **F-05** | Hash-Generierung (Prüfsummen) | SHOULD | Da SFTP standardmäßig keinen einheitlichen Hash-Befehl anbietet, soll das System versuchen, Hashes über SSH-Kommandos (z. B. `sha1sum` oder `md5sum` auf dem Remote-Host via SSH-Session) zu berechnen. |

---

## 4. Technische Schnittstellen & Architektur

### Backend-Integration
*   **Schnittstelle:** Ein neuer Adapter `SFTPProvider` implementiert das [StorageProvider-Interface](file:///c:/Users/meyer/Development/migration/backend/internal/storage/provider.go).
*   **Go-Pakete:** Verwendung der offiziellen Pakete `golang.org/x/crypto/ssh` und `github.com/pkg/sftp` für den SFTP-Client.
*   **SSH-Command-Execution (für Hashes):**
    Wenn eine Datei auf dem SFTP-Server liegt, kann ein separater SSH-Kanal geöffnet werden, um die Prüfsumme auf dem Remote-System zu ermitteln:
    ```go
    // Beispielhafter Ablauf im SFTP-Adapter zur Hash-Berechnung:
    func (p *SFTPProvider) GetFileHash(ctx context.Context, resourceType, filePath string) (string, error) {
        // Starte "sha1sum /path/to/file" über SSH-Session
        session, err := p.sshClient.NewSession()
        if err != nil {
            return "", err
        }
        defer session.Close()
        var stdout bytes.Buffer
        session.Stdout = &stdout
        err = session.Run(fmt.Sprintf("sha1sum %s", shellescape.Quote(filePath)))
        if err != nil {
            return "", err // Fallback auf Größen- und Datumsvergleich
        }
        return parseSha1SumOutput(stdout.String())
    }
    ```

### Frontend-Integration
*   Erweiterung des [ConnectForm](file:///c:/Users/meyer/Development/migration/frontend/src/components/ConnectForm.tsx):
    *   Auswahl "SFTP Server".
    *   Felder: Host, Port, Username, Passwort / Private Key.

---

## 5. Sicherheits- und Datenschutzaspekte (DSGVO)
*   **Verschlüsselung der Keyfiles:** Hochgeladene private SSH-Schlüssel werden vor dem Speichern in PostgreSQL mit AES-GCM (`ENCRYPTION_SECRET_KEY`) verschlüsselt.
*   **Sichere Chiffren:** Verwendung moderner SSH-Verschlüsselungsverfahren (AES-GCM, ChaCha20-Poly1305) und Deaktivierung veralteter, unsicherer SSH-Chiffren.

---

## 6. Akzeptanzkriterien
1. Erfolgreicher Verbindungstest sowohl mittels Passwort als auch mittels SSH-Private-Key (Passphrase-geschützt).
2. Korrekte Navigation durch verschachtelte Ordnerstrukturen im Frontend.
3. Erfolgreiche Migration von Dateien via SFTP mit korrekter Fortschrittsanzeige.
4. Korrektes Aufräumen (Schließen der SSH-Sessions und SFTP-Clients) nach Abschluss der Migration oder bei Verbindungsabbruch.
