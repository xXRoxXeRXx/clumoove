# PRD 14: Backup-Dienst (Snapshot-Backup mit Versionierung)

## 1. Einleitung & Ziel
Dieses Dokument beschreibt die Anforderungen an den **Backup-Dienst (Backup Engine)**. Im Gegensatz zu einer Migration (One-Shot) und einer Synchronisation (Zustandsabgleich) erstellt der Backup-Dienst unveränderliche Point-in-Time-Snapshots von Verzeichnisstrukturen. 

Die Ausführung wird über die [Core Scheduler Engine (PRD 06)](file:///c:/Users/meyer/Development/migration/roadmap/prd_06_schedules.md) getriggert. Der Backup-Dienst implementiert die Versionierung, Deduplizierung, Verschlüsselung und die Aufbewahrungsregeln (Retention Policies).

---

## 2. Anwendungsfälle (Use Cases)
*   **UC-1 (Schutz vor Datenverlust/Verschlüsselung):** Ein Ransomware-Angriff verschlüsselt die Quelldateien. Der Benutzer nutzt das Backup-System, um den Zustand von letztem Dienstag wiederherzustellen.
*   **UC-2 (Versehentliches Löschen):** Ein Benutzer löscht einen Ordner. Er navigiert im Backup-Explorer zum letzten wöchentlichen Snapshot und stellt den Ordner an seinem ursprünglichen Ort wieder her.

---

## 3. Funktionale Anforderungen

| ID | Anforderung | Priorität | Beschreibung |
| :--- | :--- | :--- | :--- |
| **F-01** | Point-in-Time Snapshots | MUST | Jeder Backup-Lauf erzeugt einen logischen, unveränderlichen Snapshot (eine virtuelle Kopie der Quelldateien zum Ausführungszeitpunkt). |
| **F-02** | Inkrementelles Schreiben | MUST | Physisch hochgeladen werden nur geänderte oder neue Dateien (Erkennung via Hash). Unveränderte Dateien werden im neuen Snapshot nur referenziert. |
| **F-03** | Aufbewahrungsregeln (Retention) | MUST | Automatischer GFS-Bereinigungs-Job (Grandfather-Father-Son) zur Begrenzung des Speicherplatzes (z.B. Behalten von 7 täglichen, 4 wöchentlichen, 12 monatlichen Backups). |
| **F-04** | Restore Wizard | MUST | Ein grafischer Assistent zum Durchsuchen von Snapshots und zur selektiven Wiederherstellung von Dateien/Ordnern an der Quelle oder an einem alternativen Ort. |
| **F-05** | BFS mit Schleifenschutz | MUST | Rekursives Einlesen der Quelle mittels Breitensuche (BFS) und Loop-Erkennung zur Vermeidung von Stack Overflows. |
| **F-06** | **Dauerhafter Credentials-Lifecycle** | MUST | Da Backups kontinuierlich laufen, sind die Zugangsdaten der Quell- und Ziel-Verbindungen von der automatischen 24h-Garbage-Collection (DSGVO-Cleanup) ausgeschlossen und bleiben verschlüsselt gespeichert, bis der Job manuell gelöscht wird. |

---

## 4. Technische Schnittstellen & Architektur

### Datenbank-Design
Deduplizierte Speicherung der Snapshots auf Dateiebene:

```sql
-- Speichert die Backup-Aufträge
CREATE TABLE backup_jobs (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id UUID NOT NULL,                        -- Multi-Tenancy
    source_provider TEXT NOT NULL,
    source_url TEXT NOT NULL,
    source_credentials_encrypted TEXT NOT NULL,
    target_provider TEXT NOT NULL,
    target_url TEXT NOT NULL,
    target_credentials_encrypted TEXT NOT NULL,
    encryption_salt TEXT,                         -- Für clientseitige Verschlüsselung
    is_active BOOLEAN NOT NULL DEFAULT TRUE,
    created_at TIMESTAMP WITH TIME ZONE DEFAULT CURRENT_TIMESTAMP
);

-- Speichert jeden einzelnen Backup-Durchlauf (Snapshot)
CREATE TABLE backup_snapshots (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    backup_job_id UUID NOT NULL REFERENCES backup_jobs(id) ON DELETE CASCADE,
    snapshot_time TIMESTAMP WITH TIME ZONE NOT NULL,
    status TEXT NOT NULL,                         -- 'SUCCESS', 'FAILED', 'IN_PROGRESS'
    total_files BIGINT NOT NULL DEFAULT 0,
    total_size BIGINT NOT NULL DEFAULT 0
);

-- Physische Dateien (Deduplizierung über Hash)
CREATE TABLE backup_files (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    backup_job_id UUID NOT NULL REFERENCES backup_jobs(id) ON DELETE CASCADE,
    file_hash TEXT NOT NULL,                      -- Eindeutige Identifikation der Version
    file_size BIGINT NOT NULL,
    target_path TEXT NOT NULL,                    -- Speicherpfad im Backup-Ziel
    created_at TIMESTAMP WITH TIME ZONE DEFAULT CURRENT_TIMESTAMP
);

CREATE UNIQUE INDEX idx_backup_hash ON backup_files(backup_job_id, file_hash);

-- Mapping: Welche Datei gehört zu welchem Snapshot
CREATE TABLE backup_snapshot_items (
    snapshot_id UUID NOT NULL REFERENCES backup_snapshots(id) ON DELETE CASCADE,
    backup_file_id UUID NOT NULL REFERENCES backup_files(id),
    original_path TEXT NOT NULL,                  -- Relativer Pfad an der Quelle zum Zeitpunkt des Backups
    last_modified_at TIMESTAMP WITH TIME ZONE NOT NULL,
    PRIMARY KEY (snapshot_id, original_path)
);
```

---

## 5. Sicherheits- und Datenschutzaspekte (DSGVO)
*   **Verschlüsselung der Credentials:** Alle Passwörter und Tokens werden mittels AES-GCM unter Verwendung des `ENCRYPTION_SECRET_KEY` verschlüsselt in der DB abgelegt und erst unmittelbar vor dem Verbindungsaufbau mittels `crypto.Decrypt` entschlüsselt.
*   **Clientseitige Verschlüsselung:** Möglichkeit zur lokalen Verschlüsselung (Zero-Knowledge) vor dem Upload. Der Schlüssel wird nicht auf dem Server gespeichert, wodurch Dritte keinen Zugriff auf die Backup-Dateien haben.

---

## 6. Akzeptanzkriterien
1. Backup-Läufe erzeugen neue Snapshots, laden physisch jedoch nur geänderte/neue Dateien hoch (Deduplizierung).
2. Der GFS-Bereinigungsdienst löscht überfällige Snapshots und entfernt nicht mehr referenzierte physische Dateien vom Ziel-Server.
3. Der Restore-Wizard stellt Dateien fehlerfrei aus alten Snapshots wieder her.
4. Nach Löschen des Backup-Jobs werden alle zugehörigen Metadaten, Snapshots und Credentials gelöscht.
