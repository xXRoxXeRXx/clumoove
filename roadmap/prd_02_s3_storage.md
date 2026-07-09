# PRD 02: S3-kompatibler Objektspeicher (AWS S3, Backblaze B2, MinIO, Wasabi)

## 1. Einleitung & Ziel
Dieses Dokument spezifiziert die Anforderungen zur Integration von S3-kompatiblen Objektspeichern in die Migrations-Plattform. Dies ermöglicht es Benutzern, Daten aus klassischen Cloud-Speichern (z.B. Nextcloud oder Google Drive) in kostengünstige Objektspeicher zu archivieren oder Backups von dort wiederherzustellen.

---

## 2. Anwendungsfälle (Use Cases)
*   **UC-1:** Migration eines Nextcloud-Ordners ("Archiv") in einen AWS S3 Bucket zur kostengünstigen Langzeitspeicherung.
*   **UC-2:** Wiederherstellung von Daten aus einem Backblaze B2 Bucket in ein WebDAV-Verzeichnis.
*   **UC-3:** Lokale Synchronisation zwischen einem MinIO-Server (On-Premise) und einem Public-Cloud-S3-Bucket.

---

## 3. Funktionale Anforderungen

| ID | Anforderung | Priorität | Beschreibung |
| :--- | :--- | :--- | :--- |
| **F-01** | Verbindungs-Setup | MUST | Eingabe von Endpoint-URL, Region, Access Key, Secret Key und Bucket-Name im Frontend sowie Erreichbarkeitsprüfung via API. |
| **F-02** | Bucket-Listing & Navigation | MUST | Auflistung der Objekte im Bucket, strukturiert als virtueller Verzeichnisbaum (Interpretation von `/` als Verzeichnistrenner). |
| **F-03** | Streamed Upload | MUST | Hochladen von Dateien direkt über einen Stream in den Bucket unter Verwendung von `PutObject` bzw. Multipart Upload für große Dateien. |
| **F-04** | Streamed Download | MUST | Herunterladen von Objekten über `GetObject` als Stream zum direkten Weiterleiten an das Migrationsziel. |
| **F-05** | Hash-Validierung | MUST | Validierung der Integrität anhand des ETag-Headers (MD5/Multipart-MD5) oder zusätzlichen Metadaten-Hashes (z.B. SHA-256). |

---

## 4. Technische Schnittstellen & Architektur

### Backend-Integration
*   **Adapter:** Implementierung eines `S3Provider` innerhalb des [StorageProvider-Interfaces](file:///c:/Users/meyer/Development/migration/backend/internal/storage/provider.go).
*   **SDK:** Verwendung des offiziellen `aws-sdk-go-v2` zur Interaktion mit S3 und kompatiblen APIs.
*   **Wichtige APIs:**
    *   `s3.NewFromConfig` mit benutzerdefiniertem Endpoint (wichtig für MinIO, Wasabi, Backblaze B2).
    *   `ListObjectsV2` zur Indexierung des Buckets.
    *   `manager.NewUploader` zur automatischen Handhabung von Multipart-Uploads für Dateien > 5 MB.
    *   `GetObject` zur Bereitstellung des Download-Streams.

### Frontend-Integration
*   Erweiterung des [ConnectForm](file:///c:/Users/meyer/Development/migration/frontend/src/components/ConnectForm.tsx):
    *   Auswahltyp "S3-kompatibler Speicher".
    *   Felder: Custom Endpoint (optional), Access Key, Secret Key, Region, Bucket Name.

---

## 5. Sicherheits- und Datenschutzaspekte (DSGVO)
*   **Verschlüsselung der Credentials:** AWS Secret Keys werden mittels AES-GCM (`ENCRYPTION_SECRET_KEY`) verschlüsselt in PostgreSQL abgelegt.
*   **Transit-Verschlüsselung:** Alle Verbindungen zu S3-Endpunkten müssen zwingend über HTTPS erfolgen (kann in Testumgebungen für lokale MinIO-Instanzen optional deaktiviert werden).
*   **Zero-Storage:** Keine Zwischenspeicherung auf der lokalen Festplatte des Workers. Große Multipart-Uploads werden als flüchtige In-Memory-Chunks verarbeitet.

---

## 6. Akzeptanzkriterien
1. Erfolgreicher Verbindungstest zu AWS S3, Backblaze B2 und einer lokalen MinIO-Instanz.
2. Korrekte hierarchische Darstellung der Objekte im Frontend-Dateibrowser.
3. Erfolgreiche Migration einer 1 GB großen Datei mit automatischer Multipart-Aufteilung und abschließender MD5-Hash-Validierung.
