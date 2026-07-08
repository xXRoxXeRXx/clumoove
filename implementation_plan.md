# Implementation Plan: Multi-Tenancy (Benutzerverwaltung & Login)

Dieser Plan beschreibt die technischen Änderungen zur Implementierung der Mandantenfähigkeit und Benutzerauthentifizierung auf der CloudMove-Plattform.

---

## Proposed Changes

### 1. Database (PostgreSQL) Schema & Migrations

Wir fügen die Tabellen `users` und `refresh_tokens` hinzu und verknüpfen `migrations` über einen Fremdschlüssel mit `users`.

#### [MODIFY] [schema.sql](file:///c:/Users/meyer/Development/migration/db/schema.sql)
Ergänzung des Datenbankschemas um die neuen Tabellen und den Fremdschlüssel:
```sql
CREATE TABLE IF NOT EXISTS users (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    email TEXT UNIQUE NOT NULL,
    password_hash TEXT NOT NULL,
    display_name TEXT NOT NULL,
    role TEXT NOT NULL DEFAULT 'USER',
    created_at TIMESTAMP WITH TIME ZONE DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMP WITH TIME ZONE DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE IF NOT EXISTS refresh_tokens (
    token_hash TEXT PRIMARY KEY,
    user_id UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    expires_at TIMESTAMP WITH TIME ZONE NOT NULL,
    created_at TIMESTAMP WITH TIME ZONE DEFAULT CURRENT_TIMESTAMP
);

ALTER TABLE migrations ADD COLUMN IF NOT EXISTS user_id UUID REFERENCES users(id) ON DELETE CASCADE;
CREATE INDEX IF NOT EXISTS idx_migrations_user_id ON migrations(user_id);
```

#### [MODIFY] [db.go](file:///c:/Users/meyer/Development/migration/backend/internal/db/db.go)
* Ergänzung der automatischen Tabellen- und Spaltenmigration in `InitDB`.
* Erweiterung des Structs `Migration` um das Feld `UserID string json:"user_id"`.
* Definition der neuen Structs `User` und `RefreshToken`.
* Implementierung von DB-Methoden zur Benutzerverwaltung:
  - `CreateUser(db *sql.DB, email, passwordHash, displayName string) (*User, error)`
  - `GetUserByEmail(db *sql.DB, email string) (*User, error)`
  - `GetUserByID(db *sql.DB, id string) (*User, error)`
  - `StoreRefreshToken(db *sql.DB, tokenHash string, userID string, expiresAt time.Time) error`
  - `DeleteRefreshToken(db *sql.DB, tokenHash string) error`
  - `GetUserIDByRefreshToken(db *sql.DB, tokenHash string) (string, error)`
* Ergänzung der Ownership-Verifizierung:
  - `VerifyMigrationOwnership(db *sql.DB, migrationID, userID string) (bool, error)`
* Änderung von `CreateMigration` und `GetMigration` zur Einbindung von `user_id`.
* Implementierung von `DeleteMigrationCascade(db *sql.DB, migrationID string) error` zur physischen Löschung einer Migration und ihrer Tasks.

---

### 2. Go Backend Authentication & JWT

Wir erstellen ein neues Authentifizierungs- und JWT-Verarbeitungsmodul.

#### [NEW] [auth.go](file:///c:/Users/meyer/Development/migration/backend/internal/auth/auth.go)
Kapselung der Authentifizierungslogik:
* Passwort-Hashing und -Prüfung mittels `golang.org/x/crypto/bcrypt`.
* Generierung und Validierung von JWT-Tokens (HMAC-SHA256 mit `JWT_SECRET_KEY`).
* Claims-Struktur: `UserID`, `Email`, `DisplayName`, `Role`.
* Cookie-Helper zum Setzen/Löschen des Refresh-Tokens (`HTTP-only`, `Secure`, `SameSite=Strict`).

#### [NEW] [middleware.go](file:///c:/Users/meyer/Development/migration/backend/internal/auth/middleware.go)
Authentifizierungs-Middleware für den API-Router:
* Extrahiert das JWT aus dem `Authorization: Bearer <Token>` Header.
* Validiert die Signatur und Ablaufzeit.
* Schreibt die `UserID` in den Go-Request-Context.
* Gibt `401 Unauthorized` bei ungültigem Token zurück.

---

### 3. API Gateway & Routing

#### [MODIFY] [main.go](file:///c:/Users/meyer/Development/migration/backend/cmd/api/main.go)
* **CORS-Aktualisierung:** Dynamisches Lesen des `Origin`-Headers und Setzen von `Access-Control-Allow-Credentials: true` statt `*`.
* **Entfernung des automatischen GC:** Auskommentieren oder Entfernen der Goroutine `runGarbageCollector`.
* **Neue Auth-Endpunkte einrichten:**
  - `POST /api/auth/register` (Registrierung)
  - `POST /api/auth/login` (Login, setzt Cookie)
  - `POST /api/auth/refresh` (Ausstellen eines neuen JWT via Refresh-Cookie)
  - `POST /api/auth/logout` (Löscht Session)
  - `GET /api/auth/me` (Benutzerinfo)
* **Schutz bestehender Endpunkte:**
  - Kapselung aller `/api/migration/...` Routen in der `authMiddleware`.
  - Abrufen der `userID` aus dem Request-Kontext und Filtern/Absichern aller DB-Aufrufe.
  - **DELETE-Endpunkt hinzufügen:** `DELETE /api/migration/{id}` zur manuellen Löschung.
  - **WebSocket-Absicherung:** Auslesen des JWT aus der URL (`?token=...`) in `handleWebSocket` und Inhaberschafts-Prüfung vor dem WS-Upgrade.

---

### 4. React Frontend Integration

#### [NEW] [AuthForm.tsx](file:///c:/Users/meyer/Development/migration/frontend/src/components/AuthForm.tsx)
* Responsive Login- und Registrierungs-Komponente mit Glassmorphismus-Design.
* Validiert Eingaben und speichert das Access Token nach erfolgreicher Anmeldung im React-State.

#### [NEW] [MigrationsDashboard.tsx](file:///c:/Users/meyer/Development/migration/frontend/src/components/MigrationsDashboard.tsx)
* Zeigt die Übersicht aller aktiven/abgeschlossenen Migrationen des Nutzers an.
* Bietet Optionen zum:
  - Starten einer neuen Migration (Klick leitet zum `ConnectForm` weiter).
  - Löschen einer Migration (Klick sendet `DELETE`-Request an API).
  - Anzeigen von Live-Details (öffnet bestehende Dashboard-Statistiken in einem Overlay/Modal).

#### [MODIFY] [App.tsx](file:///c:/Users/meyer/Development/migration/frontend/src/App.tsx)
* Einführung des Auth-Status (`user`, `token`).
* Interceptor/Timer-Logik zum automatischen Erneuern des JWT über `/api/auth/refresh` alle 14 Minuten.
* Routing-Guard: Wenn kein Token vorhanden ist, zeige ausschließlich `AuthForm`. Wenn eingeloggt, zeige das neue `MigrationsDashboard` bzw. die Folgeschritte (`ConnectForm`, `FileBrowser`).

---

## Verification Plan

### Automated Tests
* Ausführen von Go Unit-Tests im Auth-Modul: `go test ./backend/internal/auth/...`
* Typprüfung im Frontend: `npx tsc --noEmit --project frontend/tsconfig.json`

### Manual Verification
1. **Registrierung & Login:** Anmeldung eines Nutzers `test@example.com` über das UI. Verifizieren, dass der JWT im Speicher liegt und das Refresh-Cookie gesetzt ist.
2. **Daten-Isolation:** Einloggen mit zwei verschiedenen Browser-Fenstern (User A und User B). Starten einer Migration bei User A und Sicherstellen, dass User B diese Migration weder sieht noch über die API-Endpunkte abfragen kann.
3. **Löschfunktion:** Löschen einer Migration bei User A im Dashboard. Prüfung der PostgreSQL-Datenbank, ob Datensätze aus `migrations` und `tasks` gelöscht wurden.
4. **WebSocket-Sicherheit:** Manueller Verbindungsaufbau via WebSocket ohne Token oder mit ungültigem Token. Der Server muss die Verbindung ablehnen.
