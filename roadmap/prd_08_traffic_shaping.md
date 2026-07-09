# PRD 08: Live-Bandbreitenbegrenzung (Traffic Shaping)

## 1. Einleitung & Ziel
Große Datenübertragungen können die Netzwerkbandbreite des Migrationsservers oder der Quell- und Zielinstanzen (insb. bei selbstgehosteten Nextcloud-Instanzen an DSL-Leitungen) vollständig auslasten. Die Live-Bandbreitenbegrenzung ermöglicht es dem Benutzer, die maximale Übertragungsrate (in Megabyte pro Sekunde) dynamisch im laufenden Betrieb zu drosseln oder zu erhöhen, um Engpässe im Netzwerk zu vermeiden.

---

## 2. Anwendungsfälle (Use Cases)
*   **UC-1 (Dynamische Drosselung tagsüber):** Eine Migration läuft am Vormittag. Um den laufenden Bürobetrieb nicht zu stören, drosselt der Administrator die Übertragung live im Web-Dashboard auf `2 MB/s`.
*   **UC-2 (Vollgas nachts):** Ab 20:00 Uhr abends erhöht der Administrator das Limit wieder auf `Unbegrenzt` (bzw. das Maximum der Serverleitung).

---

## 3. Funktionale Anforderungen

| ID | Anforderung | Priorität | Beschreibung |
| :--- | :--- | :--- | :--- |
| **F-01** | Globale Bandbreitenbegrenzung | MUST | Der Worker muss die Lese- und Schreibgeschwindigkeit pro Migrations-Job begrenzen können. |
| **F-02** | Live-Änderung ohne Neustart | MUST | Der Benutzer kann das Limit im Frontend anpassen. Die Änderung wird sofort (innerhalb weniger Sekunden) vom laufenden Worker-Thread übernommen. |
| **F-03** | Frontend-Regler | MUST | Ein intuitiver UI-Schieberegler (Slider) im Dashboard mit Stufen wie: Unbegrenzt, 1 MB/s, 2 MB/s, 5 MB/s, 10 MB/s, 20 MB/s, 50 MB/s. |
| **F-04** | Unterstützung für Chunked Uploads | MUST | Die Drosselung muss sowohl beim einfachen Streaming als auch bei Chunked-Uploads (aufgeteilt in Blöcke) zuverlässig greifen. |

---

## 4. Technische Schnittstellen & Architektur

### Stream-Drosselung in Go
*   Verwendung der offiziellen Go-Bibliothek `golang.org/x/time/rate`.
*   Ein `rate.Limiter` wird für jeden Migrations-Job im Speicher des Workers verwaltet.
*   Wrapper für `io.Reader` und `io.Writer`, die vor jedem Lese- oder Schreibvorgang Tokens aus dem Limiter anfordern (`limiter.WaitN(ctx, bytesCount)`).

```go
type ThrottledReader struct {
    r       io.Reader
    limiter *rate.Limiter
    ctx     context.Context
}

func (tr *ThrottledReader) Read(p []byte) (n int, err error) {
    n, err = tr.r.Read(p)
    if n > 0 && tr.limiter != nil {
        err = tr.limiter.WaitN(tr.ctx, n)
    }
    return n, err
}
```

### Live-Kommunikation (Synchronisation)
*   Wird das Bandbreitenlimit im Frontend geändert, wird eine REST-Anfrage an das API-Gateway gesendet (z. B. `PUT /api/migration/{id}/bandwidth`).
*   Das API-Gateway speichert das neue Limit in PostgreSQL und sendet eine Pub/Sub-Nachricht über Redis an alle aktiven Worker.
*   Der betroffene Worker empfängt das Event und passt den `rate.Limiter` im laufenden Thread dynamisch an.

---

## 5. Sicherheits- und Datenschutzaspekte (DSGVO)
*   Keine Auswirkungen auf die DSGVO, da Traffic Shaping rein auf Netzwerk- und I/O-Ebene agiert und keine inhaltlichen Daten analysiert.

---

## 6. Akzeptanzkriterien
1. Wird das Limit im Frontend auf `1 MB/s` gesetzt, darf der reale Durchsatz der Migration im Worker `1,1 MB/s` nicht dauerhaft überschreiten.
2. Eine Änderung des Schiebereglers im Frontend wird innerhalb von maximal 3 Sekunden auf den aktiven Datentransfer des Workers angewendet, ohne dass die Verbindung abbricht.
3. Der Overhead der Drosselung (CPU-Zyklen für Token-Berechnungen) muss vernachlässigbar gering sein (unter 2% CPU-Last auf dem Worker-Container).
4. Das Setzen des Limits auf "Unbegrenzt" deaktiviert den Drosselungs-Wrapper komplett (Null-Overhead).
