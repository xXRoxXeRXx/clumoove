# Konzept & Architektur: Vergleich der Service-Typen

Dieses Dokument beschreibt das Zusammenspiel und die technischen Unterschiede der drei Haupt-Dienstleistungen der Plattform: **Migration**, **Synchronisation (Sync)** und **Backup**. 

Obwohl alle drei Dienste Daten über das [StorageProvider-Interface](file:///c:/Users/meyer/Development/migration/backend/internal/storage/provider.go) übertragen, unterscheiden sie sich grundlegend in ihrem Lebenszyklus, ihrer Datenhaltung und ihrem funktionalen Zweck.

---

## 1. Übersicht & Vergleichstabelle

| Kriterium | Migration (One-Shot) | Synchronisation (Sync) | Backup-Dienst (Versionierung) |
| :--- | :--- | :--- | :--- |
| **Zweck** | Einmaliger oder geplanter Umzug von Daten von A nach B. | Fortlaufender, zeitgesteuerter Abgleich zweier Speicher. | Erstellung historischer, unveränderlicher Snapshots zum Schutz vor Datenverlust. |
| **Lebenszyklus** | Temporär. Endet mit Erfolg (`COMPLETED`) oder Abbruch. | Dauerhaft. Läuft wiederkehrend, bis der Job gelöscht wird. | Geplant. Die Ausführung ist noch nicht verfügbar. |
| **Speicherung von Zugangsdaten** | Verschlüsselt für die Lebensdauer der Migration gespeichert. Terminale Migrationen werden nach 30 Tagen gelöscht. | **Dauerhaft.** Bleibt verschlüsselt gespeichert, solange der Job aktiv ist. | Noch nicht implementiert. |
| **Datenbereinigung (Cleanup)** | Löscht terminale Migrationen samt Task-Historie nach 30 Tagen. | Der Job bleibt aktiv; die aktuelle Delta-Basis wird nach einem erfolgreichen Pass atomar aktualisiert. | Noch nicht implementiert; weder Retention noch GFS existieren. |
| **Umgang mit Löschungen** | Ignoriert. Löschungen an der Quelle haben keinen Einfluss auf bereits kopierte Dateien. | Optional. Die konfigurierbare Löschpropagierung ist standardmäßig deaktiviert. | Geplant: Snapshots sollen unveränderlich sein. |
| **Versionierung** | Nein (nur Überschreiben, Überspringen oder Umbenennen bei Konflikten). | Nein (nur ein aktiver Zustand auf beiden Seiten). | Geplant; Wiederherstellung ist noch nicht verfügbar. |
| **Konfliktbehandlung** | Konflikt-Strategie (`OVERWRITE`, `SKIP`, `RENAME`). | Konflikt-Strategie (`OVERWRITE`, `SKIP`, `RENAME`) je nach Richtung; kein Restore-Modus. | Geplant. |

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
2. **Scheduler-Eintrag:** Ein Eintrag in `schedules` ohne Cron-Ausdruck wird angelegt; der nächste Lauf wird aus `interval_minutes` berechnet, sodass auch Intervalle wie 90 Minuten möglich sind.
3. **Zustandsabgleich (State Engine):** 
   - Bei jedem Start scannt die Engine beide Verzeichnisse (BFS-Scan).
   - Sie vergleicht die Dateistände mit der Tabelle `sync_state`.
   - Sie berechnet Aktionen: Datei kopieren, Datei löschen, Konflikt lösen.
4. **Ausführung & Update:** Die geänderten Dateien werden übertragen und die Tabelle `sync_state` wird mit den neuen Hashes und Größen aktualisiert.
5. **Erneutes Scheduling:** Der Scheduler berechnet `next_run_at` für die nächste Stunde. Die Zugangsdaten bleiben verschlüsselt in der DB.

### 2.3. Backup-Dienst (Point-in-Time Snapshot)
1. **Initiierung:** Ein Backup verwendet ausschließlich gespeicherte Verbindungsprofile, Quellpfade, ein fünf-Feld-Cron mit IANA-Zeitzone sowie eine Retention von 1 bis 365 Snapshots. Immich ist ausgeschlossen.
2. **Repository:** Das Ziel bleibt nach dem Erstellen unveränderlich. Clumoove legt darunter einen privaten `.clumoove-backup/<repository-id>`-Pfad mit `format-v1.json` an.
3. **Ausführung:** Der Scheduler erzeugt nur einen generation-gefenceten Run. Ein Worker übernimmt ihn, hält einen PostgreSQL-Advisory-Lock, zerlegt Dateien in 4-MiB-SHA-256-Blöcke und speichert deduplizierte, unveränderliche Packs im Clumoove-Format v1.
4. **Publishing:** Ein Snapshot bleibt bis zur Verifikation unsichtbar. Erst nach Größenprüfung der hochgeladenen Packs werden Katalog, Snapshot und Run atomar als `READY` oder `PARTIAL` veröffentlicht.

Restore, eine vollständige Repository-Prüfung, Kompression, Verschlüsselung des Archivformats und GFS-Retention gehören weiterhin zu Release 2 beziehungsweise späteren Formatversionen. Das Repository ist nicht kompatibel mit Restic oder Duplicati.

---

## 3. Datenfluss-Architektur

Das folgende Diagramm zeigt den Unterschied in der Datenaufbewahrung und dem Datenfluss:

```
[ Quelle (Server A) ] ───► [ RAM des Workers ] ───► [ Ziel (Server B) ]
                                    │
    ┌───────────────────────────────┴──────────────────────────────┐
    ▼                               ▼                              ▼
[ Migration ]                  [ Sync ]                       [ Backup ]
 - Keine Historie               - Prüft sync_state             - Erstellt Snapshots
 - Löschung nach 30 Tagen       - Aktualisiert Hashes          - Dedupliziert Blöcke je Job
 - Credentials bis zur Löschung - Credentials bleiben          - Kein Restore/GFS in Release 1
```
