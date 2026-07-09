# PRD 13: Synchronisation von Cloud-Diensten (One-Way & Two-Way Sync)

## 1. Einleitung & Ziel
Dieses Dokument spezifiziert den **Synchronisations-Dienst (Sync Engine)**. Im Gegensatz zu einer einmaligen Migration dient die Synchronisation dem fortlaufenden Abgleich zweier Cloud-Verzeichnisstrukturen. 

Der Dienst wird asynchron über die zentrale [Core Scheduler Engine (PRD 06)](file:///c:/Users/meyer/Development/migration/roadmap/prd_06_schedules.md) angestoßen und gleicht Änderungen, Neuanlagen und Löschungen zwischen Quelle und Ziel ab.

---

## 2. Anwendungsfälle (Use Cases)
*   **UC-1 (One-Way Spiegelung / Backup):** Ein Benutzer synchronisiert stündlich seine Nextcloud-Dokumente mit einem S3-Bucket. Gelöschte lokale Dateien sollen optional auch im Ziel-Bucket gelöscht werden (Spiegelung).
*   **UC-2 (Bi-direktionaler Team-Sync):** Zwei Teams arbeiten auf unterschiedlichen Plattformen (Nextcloud und Google Drive). Die Ordner werden zweiseitig synchronisiert. Konflikte werden anhand vordefinierter Regeln gelöst.

---

## 3. Funktionale Anforderungen

| ID | Anforderung | Priorität | Beschreibung |
| :--- | :--- | :--- | :--- |
| **F-01** | Sync-Modus Auswahl | MUST | Konfigurationsoption für:<br>1. *One-Way (Spiegelung)*<br>2. *Two-Way (Zweiseitiger Abgleich)*. |
| **F-02** | Deletion Propagation | MUST | Löschungsweiterleitung: Wird eine Datei an der Quelle gelöscht, wird sie im Mirror-Modus auch im Ziel gelöscht (sofern in den Job-Einstellungen aktiviert). |
| **F-03** | Konflikt-Erkennung & -Lösung | MUST | Erkennung, wenn eine Datei auf beiden Systemen seit dem letzten Sync-Lauf geändert wurde. Auflösung über Richtlinien (`source_wins`, `target_wins`, `rename_both`, `manual_resolution`). |
| **F-04** | State Tracking (Tombstones) | MUST | Speicherung eines Snapshots der Dateihashes und -pfade in der Datenbank (`sync_state`), um zu erkennen, ob eine Datei gelöscht wurde oder neu hinzugekommen ist. |
| **F-05** | Schleifenschutz beim Scan | MUST | Verwendung einer queue-basierten Breitensuche (BFS) mit Erkennung bereits besuchter Knoten (Visited-Schutz) für das Einlesen der Verzeichnisbäume, um Stack Overflows zu verhindern. |
| **F-06** | **Dauerhafter Credentials-Lifecycle** | MUST | Da der Sync-Dienst wiederkehrend läuft, sind die Zugangsdaten der Quell- und Ziel-Verbindungen von der automatischen 24h-Garbage-Collection (DSGVO-Cleanup) ausgeschlossen und bleiben verschlüsselt gespeichert, bis der Job manuell gelöscht wird. |

---

## 4. Technische Schnittstellen & Architektur

### Datenbank-Design
Der Sync-Dienst arbeitet mit einer Job-Konfiguration und einer Zustandstabelle (`sync_state`):

```sql
-- Speichert die Konfiguration der Synchronisation
CREATE TABLE sync_jobs (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id UUID NOT NULL,                        -- Multi-Tenancy-Sicherung
    source_provider TEXT NOT NULL,
    source_url TEXT NOT NULL,
    source_credentials_encrypted TEXT NOT NULL,
    target_provider TEXT NOT NULL,
    target_url TEXT NOT NULL,
    target_credentials_encrypted TEXT NOT NULL,
    sync_mode TEXT NOT NULL,                      -- 'one_way', 'two_way'
    conflict_strategy TEXT NOT NULL,              -- 'source_wins', 'target_wins', 'rename', 'manual'
    propagate_deletions BOOLEAN NOT NULL DEFAULT TRUE,
    is_active BOOLEAN NOT NULL DEFAULT TRUE,
    last_synced_at TIMESTAMP WITH TIME ZONE,
    created_at TIMESTAMP WITH TIME ZONE DEFAULT CURRENT_TIMESTAMP
);

-- Speichert den Dateistatus des letzten erfolgreichen Syncs (Snapshot)
CREATE TABLE sync_state (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    sync_job_id UUID NOT NULL REFERENCES sync_jobs(id) ON DELETE CASCADE,
    file_path TEXT NOT NULL,
    file_size BIGINT NOT NULL,
    file_hash TEXT NOT NULL,
    last_modified_at TIMESTAMP WITH TIME ZONE NOT NULL,
    updated_at TIMESTAMP WITH TIME ZONE DEFAULT CURRENT_TIMESTAMP
);

CREATE UNIQUE INDEX idx_sync_job_path ON sync_state(sync_job_id, file_path);
```

### Sync-Ablauf (ausgelöst durch den Scheduler)
1. Der Core Scheduler ruft die Sync Engine für einen Job auf.
2. Die Engine liest die Verzeichnisse von Server A und Server B ein (BFS-Scan).
3. Abgleich mit der Tabelle `sync_state`:
    *   *Datei in A vorhanden, nicht in B und nicht in sync_state:* Datei ist **neu auf A** -> Kopiere A nach B, lege Eintrag in `sync_state` an.
    *   *Datei in sync_state vorhanden, aber weder in A noch in B:* Datei wurde **auf beiden Seiten gelöscht** -> Lösche Eintrag aus `sync_state`.
    *   *Datei in sync_state vorhanden, fehlt in A, existiert in B:* Datei wurde **auf A gelöscht** -> Lösche auf B (wenn `propagate_deletions` aktiv), lösche in `sync_state`.
    *   *Datei existiert in A und B, aber Hashes weichen von sync_state ab:* **Konflikt** -> Wende `conflict_strategy` an.

---

## 5. Sicherheits- und Datenschutzaspekte (DSGVO)
*   **Verschlüsselung der Credentials:** Sämtliche Passwörter und OAuth-Tokens werden mittels AES-GCM unter Verwendung des `ENCRYPTION_SECRET_KEY` verschlüsselt gespeichert und erst im Worker kurz vor dem Verbindungsaufbau mittels `crypto.Decrypt` entschlüsselt.
*   **Aufräum-Garantie bei Deaktivierung:** Löscht der Benutzer einen Sync-Job, werden alle Zugangsdaten und die komplette Zustandstabelle `sync_state` sofort und unwiderruflich gelöscht.

---

## 6. Akzeptanzkriterien
1. Änderungen an der Quelle werden fehlerfrei zum Ziel übertragen; gelöschte Dateien werden auf dem Ziel entfernt (One-Way Sync).
2. Gleichzeitige Modifikationen auf beiden Systemen werden als Konflikt erkannt und gemäß der eingestellten Richtlinie (z. B. Umbenennung beider Dateien) gelöst.
3. Der Scan großer, tief verschachtelter Strukturen bricht dank BFS mit Schleifenschutz nicht ab.
4. Nach Deaktivierung oder Löschung des Jobs sind alle Zugangsdaten und Metadaten rückstandslos aus der Datenbank entfernt.
