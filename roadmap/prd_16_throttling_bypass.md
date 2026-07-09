# PRD 16: Auto-Throttling Bypass & Rate-Limit-Management

## 1. Einleitung & Ziel
Bei der Migration großer Datenmengen sperren Cloud-Anbieter (besonders Google Drive, OneDrive und Dropbox) temporär Zugriffe, wenn zu viele Anfragen in kurzer Zeit eingehen (HTTP 429 Too Many Requests oder API Rate Limit Exceeded). 

Das Ziel dieses Features ist es, ein intelligentes **Rate-Limit-Management** zu implementieren. Der Worker soll API-Drosselungen automatisch erkennen, die Anfragen dynamisch anpassen (Bypass/Backoff) und Techniken nutzen, um die Blockaden der Zielanbieter aktiv zu umgehen.

---

## 2. Anwendungsfälle (Use Cases)
*   **UC-1 (Automatische Drosselung bei Google Drive):** Eine Migration läuft mit hoher Geschwindigkeit. Google antwortet plötzlich mit `HTTP 429`. Der Worker pausiert die betroffene Goroutine für die von Google vorgegebene Zeit (`Retry-After`-Header) und senkt die Concurrency temporär ab, anstatt die Verbindung abbrechen zu lassen.
*   **UC-2 (IP-Rotation bei restriktiven WebDAV-Servern):** Ein selbstgehosteter Nextcloud-Server blockiert die IP des Migrations-Workers. Der Worker leitet die Anfragen über einen Pool von Proxy-Servern um, um die Blockade zu umgehen.

---

## 3. Funktionale Anforderungen

| ID | Anforderung | Priorität | Beschreibung |
| :--- | :--- | :--- | :--- |
| **F-01** | Erkennung von Rate Limits | MUST | Überwachung aller HTTP-Antworten auf Status `429` (Too Many Requests), `503` (Service Unavailable) oder spezifische API-Fehlermeldungen der Provider. |
| **F-02** | Exponential Backoff mit Jitter | MUST | Automatische Verzögerung nachfolgender Anfragen. Die Wartezeit steigt exponentiell an (z.B. 1s, 2s, 4s, 8s) und wird mit einem kleinen zufälligen Zeitversatz (Jitter) versehen, um Anfragewellen zu brechen. |
| **F-03** | Dynamisches Concurrency-Scaling | MUST | Reduzierung der aktiven parallelen Worker-Threads pro Ziel-Verbindung bei Rate-Limit-Erkennung. Sobald die API wieder stabil antwortet, wird das Limit langsam wieder erhöht (Additive Increase/Multiplicative Decrease). |
| **F-04** | User-Agent- & Header-Faking | SHOULD | Rotation von HTTP User-Agents und Request-Headern, um eine Erkennung als automatisierter Massen-Downloader zu erschweren. |
| **F-05** | Proxy-Pool-Rotation | COULD | Unterstützung für die Weiterleitung von API-Requests über wechselnde SOCKS5/HTTP-Proxys, um IP-basierte Sperren zu umgehen. |

---

## 4. Technische Schnittstellen & Architektur

### Algorithmus zur Ratenbegrenzung im HTTP-Client
Alle HTTP-Verbindungen der Storage-Provider laufen über eine einheitliche Middleware:

```go
type RateLimitRoundTripper struct {
	Proxied http.RoundTripper
	Limiter *rate.Limiter
}

func (l *RateLimitRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	// Warte, falls der interne Client-Rate-Limiter aktiv ist
	if err := l.Limiter.Wait(req.Context()); err != nil {
		return nil, err
	}

	resp, err := l.Proxied.RoundTrip(req)
	if err == nil && resp.StatusCode == 429 {
		// Lese den Retry-After Header aus
		retryAfter := resp.Header.Get("Retry-After")
		seconds, parseErr := strconv.Atoi(retryAfter)
		if parseErr == nil {
			// Pausiere den Thread für die vorgegebene Zeit
			time.Sleep(time.Duration(seconds) * time.Second)
		} else {
			// Fallback auf Exponential Backoff
			time.Sleep(5 * time.Second)
		}
	}
	return resp, err
}
```

---

## 5. Sicherheits- und Datenschutzaspekte (DSGVO)
*   **Proxy-Sicherheit:** Bei der Verwendung von Proxys zur IP-Rotation müssen diese zwingend verschlüsselt sein (HTTPS/SOCKS5 mit Auth), damit Dritte die übertragenen Datenströme nicht mitschreiben können.
*   **Daten-Transit:** Der Payload (Dateiinhalte) bleibt auch bei der Umleitung über Proxys flüchtig im RAM und wird nicht auf den Proxy-Servern zwischengespeichert.

---

## 6. Akzeptanzkriterien
1. Erhält der Worker bei einer Google-Drive-Migration ein `HTTP 429`, stürzt die Migration nicht ab, sondern pausiert kontrolliert und setzt sich nach der Drosselungszeit fort.
2. Die Thread-Anzahl für diese Migration wird im Live-Dashboard sichtbar reduziert (z. B. von 8 auf 2 parallele Verbindungen), um den Zielserver zu entlasten.
3. Nach 5 Minuten fehlerfreiem Betrieb skaliert das System die Concurrency schrittweise wieder auf das ursprüngliche Limit hoch.
4. Alle Proxy-Verbindungen sind authentifiziert und verschlüsselt.
