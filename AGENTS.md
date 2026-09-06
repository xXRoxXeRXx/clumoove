# Clumoove Agent Guidelines

Cloud-to-cloud data migration, synchronization, and backup platform.

## Quick Reference

| Task | Command |
|------|---------|
| Backend Build & Lint | `(cd backend && go build ./... && go vet ./...)` |
| Frontend Typecheck | `(cd frontend && npx tsc --noEmit --project tsconfig.app.json)` |
| Frontend Lint & Test | `(cd frontend && npx eslint src && npm test)` |
| Frontend Build | `(cd frontend && npm run build)` |

## Git Commits
- Use Conventional Commits: `<type>(<scope>): <description>` (`feat`, `fix`, `refactor`, `perf`, `style`, `test`, `docs`, `build`, `ops`, `chore`).
- Imperative lowercase present tense, no trailing period (e.g. `fix(api): validate migration ownership`). Breaking: `!` before `:` with footer.

## Core Rules & Invariants
1. **Parameterized SQL**: Always use `$1`, `$2` placeholders. Schema changes must update both [schema.sql](db/schema.sql) and `InitDB()` in [db.go](backend/internal/db/db.go).
2. **Key Segregation**: `ENCRYPTION_SECRET_KEY` exclusively for AES-256-GCM data encryption; `JWT_SECRET_KEY` exclusively for token signing. Never pass plaintext credentials to goroutines.
3. **SSRF & Egress**: Validate all user-supplied provider URLs with `storage/ssrf.go` before network egress. Plaintext HTTP is rejected.
4. **Machine-Readable Errors**: Return only typed `APIErrorCode` values — never raw `err.Error()` or English messages to API clients. Maintain `de`/`en` locale key parity.
5. **Storage Providers**: Implement `StorageProvider` in [provider.go](backend/internal/storage/provider.go) with mandatory `SupportsAtomicRename()` and `VerificationMode()`. Whitelist 17 valid providers explicitly.

## Detailed Topic Guides
- [Architecture & Daemons](.agents/architecture.md) — Monorepo entrypoints, worker schedulers, backup & restore engines, file manager.
- [Storage Providers](.agents/storage-providers.md) — Whitelist, per-provider rules, conflict resolution (`SKIP`/`OVERWRITE`/`RENAME`), 3-way hash verification.
- [Security & Authentication](.agents/security.md) — Cryptography, tokens, sessions, CORS, rate limits, headers, audit logs, 2FA, startup refusal.
- [Database & Multi-Tenancy](.agents/database.md) — Dual-update migrations, ownership checks, schedule anti-enumeration (404), BFS indexing.
- [Scheduler & Task Queue](.agents/scheduler-and-queue.md) — PostgreSQL dequeue (`SKIP LOCKED`), cron/sync trigger loop, concurrency limits, retry backoff.
- [Frontend & i18n](.agents/frontend-i18n.md) — UI styling tokens, accessibility, error translations, formatting utilities, locale parity.
- [Full Reference Docs](docs/README.md) — Complete architectural and deployment manual.
