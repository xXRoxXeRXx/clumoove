# Konzept & Architektur: Vergleich der Service-Typen

Dieses Dokument beschreibt das Zusammenspiel und die technischen Unterschiede der drei Haupt-Dienstleistungen der Plattform: **Migration**, **Synchronisation (Sync)** und **Backup**. 

Obwohl alle drei Dienste Daten über das [StorageProvider-Interface](file:///c:/Users/meyer/Development/migration/backend/internal/storage/provider.go) übertragen, unterscheiden sie sich grundlegend in ihrem Lebenszyklus, ihrer Datenhaltung und ihrem funktionalen Zweck.

---

## 1. Übersicht & Vergleichstabelle

| Kriterium | Migration (One-Shot) | Synchronisation (Sync) | Backup-Dienst (Versionierung) |
| :--- | :--- | :--- | :--- |
| **Zweck** | Einmaliger oder geplanter Umzug von Daten von A nach B. | Fortlaufender, zeitgesteuerter Abgleich zweier Speicher. | Erstellung historischer, unveränderlicher Snapshots zum Schutz vor Datenverlust. |
| **Lebenszyklus** | Temporär. Endet mit Erfolg (`COMPLETED`) oder Abbruch. | Dauerhaft. Läuft wiederkehrend, bis der Job gelöscht wird. | Dauerhaft. Läuft wiederkehrend, bis der Job gelöscht wird. |
| **Speicherung von Zugangsdaten** | **Temporär (max. 24h nach Abschluss).** Danach greift der automatische DSGVO-Clean. | **Dauerhaft.** Bleibt verschlüsselt gespeichert, solange der Job aktiv ist. | **Dauerhaft.** Bleibt verschlüsselt gespeichert, solange der Job aktiv ist. |
| **Datenbereinigung (Cleanup)** | Löscht alle Transfer-Logs und Credentials 24 Stunden nach Abschluss. | Löscht nur alte Ausführungsberichte (z. B. nach 30 Tagen). Job bleibt aktiv. | Löscht alte Snapshots basierend auf der Retention Policy (z. B. GFS-Rotation). |
| **Umgang mit Löschungen** | Ignoriert. Löschungen an der Quelle haben keinen Einfluss auf bereits kopierte Dateien. | **Wird propagiert.** Löschungen an der Quelle (A) entfernen die Datei auch am Ziel (B) (Mirror-Mode). | **Unveränderlich.** Gelöschte Dateien an der Quelle bleiben in älteren Snapshots erhalten. |
| **Versionierung** | Nein (nur Überschreiben, Überspringen oder Umbenennen bei Konflikten). | Nein (nur ein aktiver Zustand auf beiden Seiten). | **Ja.** Ermöglicht das Zurückrollen auf beliebige Zeitpunkte in der Vergangenheit. |
| **Konfliktbehandlung** | Konflikt-Strategie (`OVERWRITE`, `SKIP`, `RENAME`). | Konflikt-Richtlinie (`A_wins`, `B_wins`, `rename`, `manual`). | Keine Konflikte, da das Backup-Ziel ein schreibgeschütztes Archiv ist. |

---

## 2. Technische Funktionsweise (How it works)

### 2.1. Migration (One-Shot / Einmalig geplant)
1. **Initiierung:** Der Benutzer wählt Quelle, Ziel und Pfade und klickt auf "Start" (sofort) oder wählt einen verzögerten Zeitpunkt (z.B. heute Nacht um 02:00 Uhr).
2. **Scheduler-Eintrag:** Bei verzögertem Start wird ein Eintrag in `schedules` mit `cron_expression = NULL` und `run_at = 02:00 Uhr` angelegt.
3. **Ausführung:** Der Scheduler triggert den Job einmalig. Die Dateien werden über RAM-Buffer-Streams vom Quell- zum Zielserver übertragen.
4. **Cleanup-Phase:** 
   - Die Migration wird als `COMPLETED` markiert.
   - Ein stündlich laufender Garbage-Collector prüft abgeschlossene Migrationen.
   - Nach 24 Stunden löscht er alle Passwörter, Pfadlisten und Taskdaten aus der DB (DSGVO-Wipe).

### 2.2. Synchronisation (Sync)
1. **Initiierung:** Der Benutzer konfiguriert einen Sync-Job (z. B. stündlicher Sync zwischen Nextcloud und Google Drive).
2. **Scheduler-Eintrag:** Ein Eintrag in `schedules` mit `cron_expression = "0 * * * *"` (jede Stunde) wird angelegt.
3. **Zustandsabgleich (State Engine):** 
   - Bei jedem Start scannt die Engine beide Verzeichnisse (BFS-Scan).
   - Sie vergleicht die Dateistände mit der Tabelle `sync_state`.
   - Sie berechnet Aktionen: Datei kopieren, Datei löschen, Konflikt lösen.
4. **Ausführung & Update:** Die geänderten Dateien werden übertragen und die Tabelle `sync_state` wird mit den neuen Hashes und Größen aktualisiert.
5. **Erneutes Scheduling:** Der Scheduler berechnet `next_run_at` für die nächste Stunde. Die Zugangsdaten bleiben verschlüsselt in der DB.

### 2.3. Backup-Dienst (Point-in-Time Snapshot)
1. **Initiierung:** Der Benutzer konfiguriert ein wöchentliches Backup mit einer Aufbewahrungsregel (z. B. "Behalte 4 wöchentliche Backups").
2. **Scheduler-Eintrag:** Ein Eintrag in `schedules` mit `cron_expression = "0 2 * * 0"` (jeden Sonntag um 02:00 Uhr) wird angelegt.
3. **Snapshot-Erstellung (Deduplizierung):**
   - Die Engine liest das Quellverzeichnis ein.
   - Für jede Datei wird geprüft, ob ihr Hash bereits in der globalen Tabelle `backup_files` für diesen Job existiert.
   - *Existiert bereits:* Es wird lediglich ein neuer Verweis in `backup_snapshot_items` angelegt. Es findet **kein** physischer Upload statt (Deduplizierung).
   - *Existiert nicht:* Die Datei wird (optional clientseitig verschlüsselt) auf den Backup-Server hochgeladen und neu registriert.
4. **Retention-Bereinigung:** Nach dem Backup prüft die Engine die GFS-Regeln. Snapshots, die außerhalb des Fensters liegen, werden gelöscht. Physische Dateien in `backup_files`, die von keinem verbleibenden Snapshot mehr referenziert werden, werden vom Ziel-Server gelöscht.

---

## 3. Datenfluss-Architektur

Das folgende Diagramm zeigt den Unterschied in der Datenaufbewahrung und dem Datenfluss:

```
[ Quelle (Server A) ] ───► [ RAM des Workers ] ───► [ Ziel (Server B) ]
                                    │
    ┌───────────────────────────────┴──────────────────────────────┐
    ▼                               ▼                              ▼
[ Migration ]                  [ Sync ]                       [ Backup ]
 - Keine Historie               - Prüft sync_state             - Erstellt Snapshot-Eintrag
 - Log-Wipe nach 24h            - Aktualisiert Hashes          - Dedupliziert über Hash
 - Credentials-Wipe             - Credentials bleiben          - Bereinigt nach GFS-Regel
                                                               - Credentials bleiben
```
