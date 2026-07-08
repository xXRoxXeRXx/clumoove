# Agent Instructions

## Package Managers
- Backend: **Go** (`go build ./...`, `go vet ./...`)
- Frontend: **npm** (`npm install`, `npm run dev`, `npm run build`)

## File-Scoped Commands
| Task | Command |
|------|---------|
| Go Typecheck/Lint | `go vet backend/internal/storage/nextcloud.go` |
| TS Typecheck | `npx tsc --noEmit --project frontend/tsconfig.json` |
| JS/TS Lint | `npx eslint frontend/src/components/FileBrowser.tsx` |

## Key Conventions
- Database changes must be added to [schema.sql](file:///c:/Users/meyer/Development/migration/db/schema.sql) and programmatically auto-migrated in [db.go](file:///c:/Users/meyer/Development/migration/backend/internal/db/db.go).
- Storage providers must implement the `StorageProvider` interface in [provider.go](file:///c:/Users/meyer/Development/migration/backend/internal/storage/provider.go).
- Do not pass plaintext credentials to background goroutines. Query from database by `MigrationID` and decrypt at the last moment using `crypto.Decrypt`.
- Use queue-based Breadth-First Search (BFS) with visited loop protection for recursive directory listing/indexing to prevent stack overflow.
- **Multi-Tenancy & Isolation**: All endpoints performing operations (get, list, delete, start) on migrations must enforce ownership validation using the authenticated `UserID` from the request context.
- **Key Segregation**: Never reuse keys. Use `ENCRYPTION_SECRET_KEY` strictly for AES-GCM credential encryption/decryption, and `JWT_SECRET_KEY` strictly for JWT token signing.
- **Token Rotation**: Any token refresh request must immediately invalidate the old refresh token before generating and storing a new one.
- **CORS Whitelisting**: Never reflect incoming CORS origins or allow credentials for wildcards. Use a strict origin whitelist for credentialed requests.

