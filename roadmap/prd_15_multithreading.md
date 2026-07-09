# PRD 15: Multi-Threading & parallele Task-Verarbeitung

## 1. Einleitung & Ziel
Um den Datendurchsatz bei großen Migrationen zu maximieren, darf ein Worker nicht nur eine Datei nach der anderen sequenziell verarbeiten. Das Ziel dieses Features ist es, echtes Multi-Threading (Concurrency) auf Worker-Ebene zu implementieren. Ein einzelner Worker-Prozess soll mehrere Dateien parallel über native Go-Goroutines herunterladen, verarbeiten und hochladen.

---

## 2. Anwendungsfälle (Use Cases)
*   **UC-1 (Kleine Dateien beschleunigen):** Eine Migration besteht aus 10.000 Bildern à 1 MB. Anstatt sie nacheinander zu kopieren (was wegen des API-Overheads Stunden dauert), verarbeitet der Worker 10 oder 20 Dateien parallel. Die Migrationszeit sinkt drastisch.
*   **UC-2 (Ressourcen-Auslastung):** Ein starker Server mit viel RAM und Gigabit-Anbindung nutzt mehrere parallele Worker-Threads, um die verfügbare Bandbreite optimal auszuschöpfen.

---

## 3. Funktionale Anforderungen

| ID | Anforderung | Priorität | Beschreibung |
| :--- | :--- | :--- | :--- |
| **F-01** | Einstellbare Thread-Anzahl | MUST | Der Benutzer kann vor dem Start der Migration (oder global in den Worker-Einstellungen) die Anzahl der maximalen parallelen Transfers (Goroutines) festlegen (z.B. 1 bis 16). |
| **F-02** | Sichere Worker-Queues | MUST | Der Dequeue-Prozess aus Redis via `BRPOPLPUSH` muss absolut threadsicher sein, sodass niemals zwei Goroutines denselben Task abgreifen. |
| **F-03** | Dynamisches Worker-Pooling | MUST | Verwendung eines kontrollierten Worker-Pool-Musters (z. B. via Worker Channel oder Semaphore in Go), um die Anzahl der aktiven Goroutines hart zu begrenzen. |
| **F-04** | DB-Verbindungs-Pool | MUST | Anpassung des PostgreSQL-Verbindungs-Pools im Backend, damit bei hoher Parallelität keine Timeouts oder Verbindungsabbrüche zur Datenbank entstehen. |
| **F-05** | Ressourcenschonung | SHOULD | Begrenzung des maximalen RAM-Verbrauchs pro Thread (z. B. durch Streaming-Puffer-Größen von max. 10 MB pro Datei im RAM). |

---

## 4. Technische Schnittstellen & Architektur

### Goroutine Pool im Worker (`cmd/worker/main.go`)
Die Abarbeitung im Worker-Prozess wird über eine Worker-Group und Semaphore (Kanäle) gesteuert:

```go
func (p *Processor) StartConcurrent(ctx context.Context, maxThreads int) {
	sem := make(chan struct{}, maxThreads) // Semaphore zur Thread-Begrenzung
	var wg sync.WaitGroup

	for {
		select {
		case <-ctx.Done():
			wg.Wait()
			return
		default:
			sem <- struct{}{} // Blockiert, wenn maxThreads erreicht ist
			wg.Add(1)

			go func() {
				defer func() {
					<-sem
					wg.Done()
				}()

				// Dequeue und Task-Verarbeitung
				payload, err := p.queue.Dequeue(ctx, p.workerID, 5*time.Second)
				if err != nil || payload == nil {
					return
				}
				_ = p.processTask(ctx, payload)
			}()
		}
	}
}
```

### PostgreSQL-Pool-Einstellungen (`backend/internal/db/db.go`)
Anpassung in `InitDB`:
```go
sqlDB.SetMaxOpenConns(50) // Erlaubt bis zu 50 parallele DB-Verbindungen
sqlDB.SetMaxIdleConns(10)
sqlDB.SetConnMaxLifetime(time.Hour)
```

---

## 5. Sicherheits- und Datenschutzaspekte (DSGVO)
*   **Thread-Isolation:** Goroutines dürfen keine Shared-States für Credentials nutzen. Jede Goroutine holt sich ihre eigenen entschlüsselten Credentials frisch aus der DB, verarbeitet den Stream und verwirft die Daten sofort nach Abschluss aus dem Arbeitsspeicher.
*   **Verschlüsselung:** Alle Entschlüsselungsvorgänge nutzen die AES-GCM-Logik mit dem dedizierten `ENCRYPTION_SECRET_KEY` und sind threadsicher.

---

## 6. Akzeptanzkriterien
1. Wird das Limit auf `5 Threads` gesetzt, laufen exakt 5 Dateien parallel im Dateitransfer, was sich im Live-Dashboard und den Systemlogs verifizieren lässt.
2. Es kommt zu keinem Zeitpunkt zu Race-Conditions beim Dequeuen aus Redis (jeder Task wird exakt einmal verarbeitet).
3. Der RAM-Verbrauch des Workers skaliert linear mit der Anzahl der Threads und bleibt innerhalb der definierten Puffergrenzen (max. 10 MB pro Thread).
4. Bei plötzlichem Verbindungsabbruch werden alle aktiven Threads sauber abgebrochen und die unvollständigen Tasks zurück in Redis eingereiht.
