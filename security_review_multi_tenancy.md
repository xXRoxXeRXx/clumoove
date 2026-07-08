# Security Review & Viability Analysis: Multi-Tenancy on CloudMove

Dieses Dokument bewertet die Umsetzbarkeit des Multi-Tenancy-PRDs im Kontext des aktuellen Codebestands und hebt kritische sicherheitsrelevante Aspekte sowie deren Implementierungs-Empfehlungen hervor.

---

## 🟥 1. Kritische Sicherheitsaspekte (CRITICAL)

### 1.1 Local Directory Adapter & Path Traversal (SaaS-Sicherheitsrisiko)
* **Problem:** Der mit dem letzten Release eingeführte lokale Speicher-Adapter (`local.go`) ermöglicht Lese- und Schreibzugriffe auf Pfade des Host-Systems, die in den Docker-Containern (`shared_data:/data`) gemountet sind. In einer SaaS-Umgebung könnten Benutzer durch Eingabe von Pfaden wie `../../` oder `/data/other-user-uuid` Zugriff auf fremde Daten erlangen.
* **Maßnahme:** 
  1. **Sandbox-Pflicht:** Der Dateisystem-Adapter darf Pfade nicht ungefiltert akzeptieren. Jeder Pfad muss serverseitig in ein benutzerspezifisches Unterverzeichnis gezwungen werden:
     ```go
     // Beispiel für die Pfad-Validierung im Local-Adapter
     userSandbox := filepath.Join("/data/users", userID)
     targetPath := filepath.Clean(filepath.Join(userSandbox, userInputPath))
     if !strings.HasPrefix(targetPath, userSandbox) {
         return errors.New("access denied: path traversal attempt detected")
     }
     ```
  2. **Feature-Flagging:** Alternativ sollte der lokale Dateisystem-Adapter für die SaaS-Instanz per Umgebungsvariable (`ENABLE_LOCAL_STORAGE=false`) deaktiviert werden können.

### 1.2 WebSocket Authentifizierung (`/api/migration/{id}/ws`)
* **Problem:** WebSockets unterstützen bei der Initialisierung im Browser über `new WebSocket(url)` keine benutzerdefinierten Header (wie `Authorization: Bearer <JWT>`). Wenn der WS-Endpunkt nicht geschützt wird, kann jeder beliebige Angreifer über die UUID der Migration den Live-Fortschritt und Dateipfade abhören.
* **Maßnahme:**
  - Token-Passing via Query-Parameter: `/api/migration/{id}/ws?token=JWT_ACCESS_TOKEN`.
  - Im Go-Backend muss die WS-Handler-Funktion (`handleWebSocket` in [main.go](file:///c:/Users/meyer/Development/migration/backend/cmd/api/main.go#L811-L837)) den Query-Parameter `token` auslesen, kryptografisch verifizieren und prüfen, ob die darin enthaltene `user_id` der `user_id` der angeforderten Migration entspricht. Erst bei Erfolg darf `upgrader.Upgrade` aufgerufen werden.

### 1.3 CORS-Konfiguration bei HTTP-only Cookies
* **Problem:** Die aktuelle CORS-Middleware in [main.go](file:///c:/Users/meyer/Development/migration/backend/cmd/api/main.go#L148-L162) nutzt Wildcards: `w.Header().Set("Access-Control-Allow-Origin", "*")`. 
  - Wenn wir das Refresh-Token in einem HTTP-only Cookie speichern, verweigern Browser das Senden von Cookies, wenn der Origin-Header ein Wildcard (`*`) ist.
* **Maßnahme:** Die CORS-Middleware muss dynamisch die anfragende Herkunft (`Origin`) prüfen und zurückgeben. Zudem muss `Access-Control-Allow-Credentials: true` gesetzt werden:
  ```go
  origin := r.Header.Get("Origin")
  if isAllowedOrigin(origin) {
      w.Header().Set("Access-Control-Allow-Origin", origin)
      w.Header().Set("Access-Control-Allow-Credentials", "true")
  }
  ```

---

## 🟡 2. Wichtige Implementierungspunkte (IMPORTANT)

### 2.1 Anpassung der Datenbank-Hilfsfunktionen in `db.go`
* **Vorsicht bei globalen Queries:** Die Funktionen `GetMigration`, `UpdateMigrationStatus` und `IncrementMigrationProgress` in [db.go](file:///c:/Users/meyer/Development/migration/backend/internal/db/db.go) dürfen nicht ungesichert bleiben.
* **Lösung:**
  - **API-Layer:** Muss immer den Benutzerkontext prüfen. Es empfiehlt sich, eine Helper-Funktion zu schreiben:
    ```go
    func VerifyMigrationOwnership(db *sql.DB, migrationID, userID string) (bool, error) {
        var exists bool
        query := `SELECT EXISTS(SELECT 1 FROM migrations WHERE id = $1 AND user_id = $2)`
        err := db.QueryRow(query, migrationID, userID).Scan(&exists)
        return exists, err
    }
    ```
  - Vor jedem GET, DELETE oder WebSocket-Upgrade auf eine Migration muss diese Prüfung vorgeschaltet sein.

### 2.2 Worker-Sicherheit (Redis Queue)
* Die Architektur ist sauber entkoppelt: In Redis werden nur `migration_id` und `task_id` abgelegt. Der Worker holt sich die Details direkt über PostgreSQL und entschlüsselt die Anmeldedaten erst im flüchtigen Speicher des Workers (`crypto.Decrypt`).
* **Sicherheitskonformität:** Da der Worker im internen Netz läuft und direkt mit der PostgreSQL-Datenbank kommuniziert, muss er nicht mandantenfähig eingeschränkt werden. Seine Aufgabe ist das reine Abarbeiten der SQL-Einträge. Die Autorisierung findet bereits beim Enqueuing im API-Gateway statt.

### 2.3 Datenbank-Wachstum bei Deaktivierung des GCs
* **Problem:** Da der automatische 24h-Hintergrund-Garbage-Collector vollständig aus dem Code entfernt wird, wächst die Datenbank durch detaillierte Dateiprotokolle (`tasks`-Tabelle, oft mehrere Tausend Einträge pro Migration) permanent an.
* **Konsequenzen & Maßnahmen:**
  1. **User-driven Cleanup:** Die Löschung liegt in der vollen Verantwortung des Benutzers. Im UI sollte ein deutlicher Hinweis platziert werden, der dazu anregt, abgeschlossene Migrationen nach erfolgreichem Transfer zu löschen.
  2. **Storage Provisioning:** Da Daten dauerhaft verbleiben, müssen System-Administratoren die Festplattenkapazität der PostgreSQL-Instanz proaktiv skalieren.
  3. **Index-Optimierung:** Die Indizes auf `tasks(migration_id)` und `tasks(status)` müssen robust sein, da die Anzahl der Zeilen in der `tasks`-Tabelle bei vielen Benutzern schnell in die Millionen gehen kann.

---

## 🔵 3. Nitpicks & UX-Hinweise (NITPICK)

* **JWT Secret Rotation:** Auf Produktionsumgebungen muss sichergestellt sein, dass das `JWT_SECRET_KEY` über Umgebungsvariablen geladen wird. Ein Fallback auf einen zufälligen In-Memory-Key bei jedem Serverstart (wie bei der Single-User-Verschlüsselung manchmal üblich) würde dazu führen, dass alle Benutzer bei jedem API-Redeployment ausgeloggt werden.
* **Kaskadierendes Löschen:** Stellen Sie sicher, dass beim Löschen einer Migration (`DELETE /api/migration/{id}`) im UI ein Bestätigungs-Dialog angezeigt wird, da das Löschen unwiderruflich ist.

---

## Fazit zur Umsetzbarkeit

Die geplante Multi-Tenancy-Architektur ist mit dem aktuellen Go/React-Stack **vollständig und elegant umsetzbar**.
* Das Datenmodell lässt sich sauber über relationale Fremdschlüssel in PostgreSQL abbilden.
* Das Go-Backend (Go 1.22 Router) bietet mit standardmäßigen HTTP-Middlewares eine performante Möglichkeit, JWTs zu validieren und in den Request-Context zu injizieren.
* Der Übergang zu einem persistenten Dashboard mit Löschfunktion erhöht den Nutzwert für SaaS-Anwender erheblich und gibt dem Nutzer die volle Kontrolle über seine Daten.
