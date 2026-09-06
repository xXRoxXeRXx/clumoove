# Database & Multi-Tenancy

## Schema Migrations & Queries
- **Dual-Update Contract**:
  - Every schema change must be added to [db/schema.sql](../db/schema.sql) for clean installs.
  - The same change must also be added as an idempotent `CREATE TABLE IF NOT EXISTS` or `ALTER TABLE … ADD COLUMN IF NOT EXISTS` statement inside `InitDB()` in [backend/internal/db/db.go](../backend/internal/db/db.go) for automatic startup migrations.
- **Parameterized SQL**: All database operations must use parameterized placeholders (`$1`, `$2`, …). Never format or concatenate untrusted inputs into SQL strings.

## Multi-Tenancy & Ownership Enforcement
- **Resource Ownership**:
  - Migration endpoints (`GET /api/migration/{id}`, `DELETE /api/migration/{id}`, `GET /api/migration/{id}/report`, SSE stream) must retrieve the caller's ID via `auth.GetUserIDFromContext(r.Context())` and compare with `migration.UserID`. Return `403 Forbidden` on mismatch.
  - The User ID must always originate from validated JWT claims (`auth.ClaimsKey`).
- **Schedule Anti-Enumeration**:
  - Schedule endpoints (`GET /api/schedule/{id}`, `DELETE /api/schedule/{id}`) verify ownership with `db.VerifyScheduleOwnership` (which uses SQL `EXISTS`).
  - If ownership fails or the row does not exist, return `404 Not Found` in both cases. Never return `403 Forbidden` for schedules, preventing cross-tenant existence enumeration.
- **Profile References**: Persist `source_profile_id` and `target_profile_id` foreign keys with `ON DELETE SET NULL`.

## Directory Indexing (BFS)
- **Traversal Algorithm**: Use queue-based Breadth-First Search (BFS) with an in-memory `visited` map to safely traverse nested hierarchies without looping on symlink cycles or circular WebDAV mounts.
- **Deduplication**: Track indexed items with a `resourceType:path` composite key in an `indexedPaths` map to guarantee task uniqueness.
- **Error Resilience**:
  - Individual folder/file read errors are recorded in `indexing_errors` and skipped; a single inaccessible path must never abort the overall migration.
  - Errors are surfaced in the final migration report.
  - `sanitizeError` must redact credentials (`user:pass@`) from embedded URLs before persisting to `migrations.error_message` or `indexing_errors`.
