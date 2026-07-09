# PRD 19: Metadaten-Erhalt (Metadata Preservation)

## 1. Einleitung & Ziel
Beim Kopieren von Dateien über Standardprotokolle gehen oft wertvolle Metadaten verloren (z. B. Erstellungsdatum, Tags, Farbmarkierungen oder Beschreibungen). 
Das Ziel dieses Features ist es, dateispezifische Metadaten beim Auslesen aus der Quelle zu sichern und auf dem Ziel-System wiederherzustellen, damit die Organisation und Suchbarkeit der Dateien nach dem Umzug erhalten bleibt.

---

## 2. Anwendungsfälle (Use Cases)
*   **UC-1 (Nextcloud Tags zu Google Drive Labels):** Ein Benutzer hat in seiner Nextcloud Dateien mit den Tags `#Rechnung` und `#Wichtig` versehen. Bei der Migration zu Google Drive werden diese in entsprechende Drive-Labels übersetzt.
*   **UC-2 (Präzise Zeitstempel):** Eine Fotobibliothek wird migriert. Es ist zwingend erforderlich, dass das ursprüngliche Erstellungsdatum (Creation Date) und Änderungsdatum (Modification Date) auf dem Zielsystem exakt dem Original entsprechen, damit Bild-Sortierungen nach Datum weiterhin stimmen.

---

## 3. Funktionale Anforderungen

| ID | Anforderung | Priorität | Beschreibung |
| :--- | :--- | :--- | :--- |
| **F-01** | Erhalt von Zeitstempeln | MUST | Die Quellzeitstempel (`Last Modified`, optional `Created At`) müssen ausgelesen und auf dem Zielsystem via API gesetzt werden (z. B. WebDAV `PROPPATCH` oder Google Drive API `modifiedTime`). |
| **F-02** | Tag- und Label-Mapping | SHOULD | Auslesen von Nextcloud-Tags oder Google-Drive-Labels und Konvertierung in das jeweils andere Format. |
| **F-03** | Datei-Beschreibungen | SHOULD | Übernahme von Dateibeschreibungen (Google Drive `description` oder Nextcloud Kommentare) und Speicherung im Ziel. |
| **F-04** | Fallback auf Sidecar-Dateien | COULD | Wenn das Zielsystem keine Metadaten unterstützt (z. B. ein einfacher FTP-Server), können die Metadaten optional in einer JSON-Begleitdatei (Sidecar-Datei, z. B. `foto.jpg.json`) neben der Datei abgelegt werden. |

---

## 4. Technische Schnittstellen & Architektur

### Speicher-Struktur für Metadaten in Go
Erweiterung des `CloudResource`-Structs in [provider.go](file:///c:/Users/meyer/Development/migration/backend/internal/storage/provider.go):

```go
type FileMetadata struct {
	CreatedTime time.Time         `json:"created_time,omitempty"`
	Description string            `json:"description,omitempty"`
	Tags        []string          `json:"tags,omitempty"`
	Starred     bool              `json:"starred,omitempty"`
	CustomProps map[string]string `json:"custom_props,omitempty"`
}

type CloudResource struct {
	Path         string       `json:"path"`
	Name         string       `json:"name"`
	Size         int64        `json:"size"`
	IsDir        bool         `json:"is_dir"`
	Hash         string       `json:"hash"`
	LastModified time.Time    `json:"last_modified"`
	Metadata     FileMetadata `json:"metadata"` // Hinzugefügte Metadaten-Struktur
}
```

### Implementierungs-Details
*   **Nextcloud / WebDAV:** Das Setzen des Änderungsdatums erfolgt über die WebDAV-Methode `PROPPATCH` unter Angabe des XML-Tags `<d:lastmodified>`.
*   **Google Drive:** Beim Upload über die Drive-API wird das Feld `keepRevisionForever` und `modifiedTime` explizit im Payload übergeben.
*   **Ablauf im Worker:** Der Worker zieht die Metadaten beim Einlesen der Quelle ab (Indexierungsphase) und speichert diese in der DB in der Tabelle `tasks` (als JSONB-Spalte). Beim Hochladen übergibt der Worker diese Metadaten an die Upload-Methode des Ziel-Adapters.

---

## 5. Sicherheits- und Datenschutzaspekte (DSGVO)
*   **Datenminimierung:** Es werden keine Dateiinhalte in den Metadaten erfasst.
*   **Cleanup:** Die Metadaten-Spalte in der Tabelle `tasks` wird im Zuge der 24h-Datenbereinigung vollständig gelöscht.

---

## 6. Akzeptanzkriterien
1. Nach der Migration einer Datei von Nextcloud zu Google Drive weicht das im Ziel-Dateibrowser angezeigte Änderungsdatum um maximal 1 Sekunde vom Original ab.
2. Nextcloud-Tags werden erfolgreich ausgelesen und als Labels auf Google Drive-Dateien angewendet (sofern die Ziel-API dies erlaubt).
3. Wenn ein Ziel-System das Setzen von Änderungsdaten blockiert, schlägt der Transfer nicht fehl, sondern loggt eine Warnung und behält das aktuelle Upload-Datum bei.
