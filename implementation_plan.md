# Goal: Erweiterung um einen weiteren Dienst (Lokales Dateisystem)

Wir erweitern die Multi-Cloud Migrations-Plattform um die Anbindung von **Lokalen Verzeichnissen** (Local Directory / Filesystem) auf dem Server. Dies ermöglicht:
1. Migrationen von einem lokalen Serverpfad in eine Nextcloud-Instanz (Upload/Backup-Import).
2. Migrationen von Nextcloud in ein lokales Serverpfzeichnis (Backup/Archivierung).
3. Migrationen zwischen lokalen Serverpfaden (Local-to-Local für Tests und Synchronisation).

Um dies sauber zu implementieren, modularisieren wir das Backend durch die Einführung eines `StorageAdapter`-Interfaces. Das Frontend wird um Auswahlfelder für den Typ der Quelle und des Ziels (Nextcloud WebDAV vs. Lokales Verzeichnis) ergänzt.

---

## User Review Required

> [!IMPORTANT]
> **Pfad-Zugriff in Docker:**
> Die lokalen Verzeichnisse müssen sich in Pfaden befinden, die für die Docker-Container (`api-backend` und `migration-worker`) zugänglich sind. Standardmäßig ist das Verzeichnis `/app` (gemountet auf das `./backend`-Hostverzeichnis) zugänglich. 
> Wir werden ein gemeinsames Volume `./shared_data:/data` in die Docker-Dienste einbinden, damit Benutzer bequem `/data/source` und `/data/target` auf ihrem Server nutzen können.

---

## Open Questions

Keine offenen Fragen. Die Implementierung nutzt das bewährte, RAM-schonende Streaming-Konzept des Workers ohne lokale Plattencaches für Transferdaten.

---

## Proposed Changes

### 1. Database (PostgreSQL) Schema-Erweiterung

Wir erweitern die Tabelle `migrations` um Typ-Spalten, um zu speichern, ob die Quelle/das Ziel ein Nextcloud- oder ein lokales Verzeichnis ist.

#### [MODIFY] [schema.sql](file:///c:/Users/meyer/Development/migration/db/schema.sql)
Ergänzung der Standardwerte und Typen. Da die DB bei bestehendem Setup nicht neu initialisiert wird, führen wir die Migration automatisch in Go beim Anwendungsstart (`InitDB`) aus:
```sql
ALTER TABLE migrations ADD COLUMN IF NOT EXISTS source_type TEXT NOT NULL DEFAULT 'nextcloud';
ALTER TABLE migrations ADD COLUMN IF NOT EXISTS target_type TEXT NOT NULL DEFAULT 'nextcloud';
```

---

### 2. Go Backend (Refactoring & Neuer Adapter)

Wir führen das Interface `StorageAdapter` ein und implementieren den lokalen Dateisystem-Adapter.

#### [NEW] [storage.go](file:///c:/Users/meyer/Development/migration/backend/internal/storage/storage.go)
Definiert das gemeinsame Interface für alle Speicher-Provider:
- `Connect(ctx context.Context) (bool, error)`
- `GetDirectoryListing(ctx context.Context, dirPath string) ([]CloudFile, error)`
- `StreamDownload(ctx context.Context, filePath string) (io.ReadCloser, error)`
- `StreamUpload(ctx context.Context, filePath string, stream io.Reader, size int64) error`
- `StreamUploadChunked(ctx context.Context, filePath string, stream io.Reader, size int64, progressChan chan<- int64) error`
- `FileExists(ctx context.Context, filePath string) (bool, int64, error)`
- `DeleteFile(ctx context.Context, filePath string) error`
- `GetFileHash(ctx context.Context, filePath string) (string, error)`

#### [NEW] [local.go](file:///c:/Users/meyer/Development/migration/backend/internal/storage/local.go)
Implementiert `StorageAdapter` für das lokale Dateisystem:
- `Connect`: Prüft, ob das angegebene Stammverzeichnis existiert und beschreibbar ist (erstellt es falls nötig).
- `GetDirectoryListing`: Liest das Verzeichnis rekursiv bzw. flach mittels `os.ReadDir`.
- `StreamDownload`: Öffnet die Datei mit `os.Open` zum Streamen.
- `StreamUpload`: Schreibt den eingehenden Stream direkt via `os.OpenFile` und `io.Copy` auf die Platte (Zero Memory Overhead).
- `StreamUploadChunked`: Fällt für lokale Dateien auf das direkte Schreiben zurück (triggert Fortschritt über den Channel).
- `GetFileHash`: Berechnet den SHA-1 Hash der lokalen Datei direkt auf der Platte.

#### [NEW] [factory.go](file:///c:/Users/meyer/Development/migration/backend/internal/storage/factory.go)
Stellt die Factory-Methode bereit, um den passenden Adapter zu instanziieren:
```go
func NewAdapter(providerType, urlStr, username, password string) (StorageAdapter, error)
```

#### [MODIFY] [client.go](file:///c:/Users/meyer/Development/migration/backend/internal/webdav/client.go)
- Verschieben des Structs `CloudFile` in das `storage`-Paket (bzw. Aliasing), damit es providerunabhängig genutzt werden kann.
- Anpassen der Methodensignaturen von `Client` an das `StorageAdapter`-Interface (z. B. `StreamDownload` gibt kein `http.Header` mehr zurück, da dies WebDAV-spezifisch ist).

#### [MODIFY] [db.go](file:///c:/Users/meyer/Development/migration/backend/internal/db/db.go)
- Ergänzung der Felder `SourceType` und `TargetType` im `Migration`-Struct.
- Ausführung von `ALTER TABLE migrations ADD COLUMN IF NOT EXISTS ...` in `InitDB` zur automatischen Migration der Datenbankstruktur.
- Anpassung von `CreateMigration` und `GetMigration` an die neuen Spalten.

#### [MODIFY] [processor.go](file:///c:/Users/meyer/Development/migration/backend/internal/processor/processor.go)
- Verwendung von `storage.NewAdapter(...)` anstelle von direktem `webdav.NewClient`.
- Deklaration der Clients als `storage.StorageAdapter` anstelle von `*webdav.Client`.

#### [MODIFY] [main.go](file:///c:/Users/meyer/Development/migration/backend/cmd/api/main.go)
- Anpassung der Endpunkte `/api/migration/connect` und `/api/migration/start` an die neuen Felder `source_type` und `target_type`.
- Verwendung des `storage.StorageAdapter` Interfaces zum Ordner-Scanning in `startIndexing` und `indexFolder`.

---

### 3. Frontend Component (React SPA)

#### [MODIFY] [ConnectForm.tsx](file:///c:/Users/meyer/Development/migration/frontend/src/components/ConnectForm.tsx)
- Hinzufügen eines Dropdowns (`sourceType` / `targetType`) für Quelle und Ziel mit den Optionen:
  - **Nextcloud** (WebDAV)
  - **Lokales Verzeichnis** (Local Path)
- Wenn "Nextcloud" ausgewählt ist: Zeige URL, Benutzername und Passwort.
- Wenn "Lokales Verzeichnis" ausgewählt ist: Zeige nur ein Eingabefeld für den lokalen Pfad (z. B. `/data/source`) und blende Benutzername/Passwort aus.
- Senden der neuen Parameter `source_type` und `target_type` an die API.

#### [MODIFY] [FileBrowser.tsx](file:///c:/Users/meyer/Development/migration/frontend/src/components/FileBrowser.tsx)
- Keine funktionalen Änderungen nötig, da es die in `ConnectForm` gesetzten Zugangsdaten (`credentials`) transparent weiterreicht.

---

### 4. Infrastructure (Docker Compose)

#### [MODIFY] [docker-compose.yml](file:///c:/Users/meyer/Development/migration/docker-compose.yml)
- Einrichten eines gemeinsamen lokalen Speicherordners `./shared_data:/data` in den Containern `api-backend` and `migration-worker`. Dies erleichtert das Testen und Verwenden des lokalen Adapters erheblich.

---

## Verification Plan

### Automated Tests
- Wir führen nach der Implementierung die Go-Kompilierungsprüfungen (`go vet ./...`) durch.
- Erstellung eines Integrationstests `storage_test.go` im Backend zur Verifizierung der Lese- und Schreibvorgänge des `LocalAdapter`.

### Manual Verification
1. **Docker rebuild & up:**
   ```bash
   docker compose down
   docker compose up --build -d
   ```
2. **Local-to-Local Test:**
   - Quelle: Lokales Verzeichnis `/data/source` (befüllt mit ein paar Testdateien).
   - Ziel: Lokales Verzeichnis `/data/target`.
   - Starten der Migration im Frontend und Prüfung, ob die Dateien korrekt gestreamt und die Hashes validiert wurden.
3. **Local-to-Nextcloud Test:**
   - Migration von `/data/source` in die Nextcloud.
4. **Nextcloud-to-Local Test:**
   - Herunterladen von Nextcloud in `/data/target`.
