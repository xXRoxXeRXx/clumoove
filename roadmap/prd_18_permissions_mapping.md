# PRD 18: Berechtigungs-Mapping (Permissions Mapping)

## 1. Einleitung & Ziel
Unterschiedliche Cloud-Speicher nutzen grundlegend verschiedene Modelle zur Berechtigungsvergabe (z. B. POSIX-Rechte bei SFTP, ACLs bei Windows/SMB, Freigabelinks bei Nextcloud und Google Drive). 
Das Ziel dieses Features ist es, Freigabe- und Zugriffsberechtigungen während der Migration intelligent zu übersetzen, damit Benutzer nach dem Umzug dieselben Lese- und Schreibrechte auf ihren Dateien vorfinden wie auf dem Quellsystem.

---

## 2. Anwendungsfälle (Use Cases)
*   **UC-1 (Nextcloud zu Google Drive Freigaben):** Ein Nextcloud-Ordner ist für den Benutzer `max.mustermann` (Lesen) und `clara.muster` (Schreiben) freigegeben. Bei der Migration zu Google Drive übersetzt das System diese Freigaben in Google Drive API Berechtigungen ("Reader" für Max, "Writer" für Clara) basierend auf einer E-Mail-Mapping-Tabelle.
*   **UC-2 (POSIX zu Nextcloud ACLs):** Dateien von einem Linux-SFTP-Server mit den Berechtigungen `644` (Eigentümer schreiben/lesen, alle anderen nur lesen) werden in Nextcloud so importiert, dass die entsprechenden Nextcloud-Benutzergruppen analoge Lese- und Schreibrechte erhalten.

---

## 3. Funktionale Anforderungen

| ID | Anforderung | Priorität | Beschreibung |
| :--- | :--- | :--- | :--- |
| **F-01** | User Mapping Table | MUST | Ein UI-Interface, in dem der Administrator Quell-Benutzernamen/-E-Mails den entsprechenden Ziel-Benutzernamen/-E-Mails zuordnen kann (z.B. `nextcloud_user1` -> `user1@gsuite-domain.com`). |
| **F-02** | Freigaben auslesen (Source) | MUST | Die Quell-Adapter müssen in der Lage sein, Freigabe-Metadaten auszulesen (z. B. Nextcloud Share API, Google Drive Permissions API). |
| **F-03** | Freigaben anwenden (Target) | MUST | Die Ziel-Adapter müssen in der Lage sein, Berechtigungen über die jeweilige API zu setzen (z. B. `driveService.Permissions.Create`). |
| **F-04** | Fallback-Regeln | MUST | Definition von Standard-Verhalten, wenn ein Quell-Benutzer im Ziel-System nicht existiert (z. B. *Überspringen*, *Rechte an Admin übertragen* oder *Warnung protokollieren*). |
| **F-05** | Public Shares (Link-Freigaben) | SHOULD | Übersetzung öffentlicher Freigabelinks (z. B. Passwortgeschützte Links in Nextcloud zu analogen Google Drive Links, ggf. mit Generierung neuer Passwörter und Benachrichtigung). |

---

## 4. Technische Schnittstellen & Architektur

### Erweiterung des StorageProvider-Interfaces
Es werden neue optionale Methoden im [StorageProvider-Interface](file:///c:/Users/meyer/Development/migration/backend/internal/storage/provider.go) definiert:

```go
type ResourcePermission struct {
	UserEmail string `json:"user_email"` // Eindeutiger Bezeichner im Ziel
	Role      string `json:"role"`       // 'owner', 'writer', 'reader'
	IsPublic  bool   `json:"is_public"`  // Freigabelink
}

// Optionale Interfaces für Provider, die Berechtigungen unterstützen:
type PermissionsReader interface {
	GetPermissions(ctx context.Context, resourceType, filePath string) ([]ResourcePermission, error)
}

type PermissionsWriter interface {
	SetPermissions(ctx context.Context, resourceType, filePath string, perms []ResourcePermission) error
}
```

### Ablauf im Migrations-Worker
1. Worker lädt Datei herunter und lädt sie zum Ziel hoch.
2. Wenn Quelle `PermissionsReader` und Ziel `PermissionsWriter` implementieren:
   - Worker ruft `GetPermissions` für die Quelldatei ab.
   - Worker gleicht die Benutzer anhand der in der DB hinterlegten `user_mapping_table` ab.
   - Worker ruft `SetPermissions` auf dem Ziel-System auf.
3. Schlägt das Mapping für einen Benutzer fehl, wird ein Warnungseintrag im Fehlerbericht (PRD 07) erzeugt, aber die Datei selbst wird erfolgreich übertragen.

---

## 5. Sicherheits- und Datenschutzaspekte (DSGVO)
*   **Datenschutz:** Berechtigungs-Mappings enthalten sensible E-Mail-Adressen und Benutzernamen. Diese Daten werden strikt isoliert per `user_id` gespeichert und unterliegen derselben 24h-Wipe-Garantie für abgeschlossene Migrationen.
*   **Sicherheitsrisiko (Over-Sharing):** Das System muss verhindern, dass private Daten durch fehlerhaftes Mapping öffentlich freigegeben werden. Im Zweifelsfall (z. B. ungültige Zuordnung) gilt das Prinzip: *Sicherer Standard = Keine Freigabe*.

---

## 6. Akzeptanzkriterien
1. Berechtigungen (Lesen/Schreiben) werden beim Umzug Nextcloud-zu-Google-Drive anhand der Mapping-Tabelle korrekt auf die Ziel-Dateien übertragen.
2. Ungültige Benutzerzuordnungen blockieren nicht die Migration, sondern werden als Warnung im Protokoll eingetragen.
3. Öffentliche Links werden auf dem Ziel-System neu generiert und in den Logdateien ausgegeben.
