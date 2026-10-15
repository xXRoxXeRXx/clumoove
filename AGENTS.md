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

<!-- OPENWIKI:START -->

## OpenWiki

This repository has a generated `openwiki/` evidence index. It is optional just-in-time context, not required startup reading.

- Do not enumerate, preload, or search wikis at task start. Use retrieval when the user asks for it, when unfamiliar architecture or dependency behavior materially affects the task, or when source inspection leaves an important uncertainty. Stop once the question is grounded.
- When those conditions apply and OpenWiki retrieval tools are available, use `openwiki_search` for just-in-time context and `openwiki_read` for the relevant complete sections. If search returns `workspace_required`, ask which listed workspace to use and retry with its ID.
- Use `openwiki_list_workspaces` or `openwiki_list_wikis` when workspace membership itself needs to be discovered.
- If the retrieval tools are unavailable, read `openwiki/quickstart.md` and follow its links to the relevant pages.
- Treat source code and tests as authoritative. A brief's unknowns and review items are verification gaps, not automatic requirements.
- Prefer the narrowest quiet validation that proves the changed behavior. Preserve complete failure output.

The scheduled OpenWiki GitHub Actions workflow refreshes the repository wiki. Do not hand-edit generated OpenWiki pages unless explicitly asked; prefer updating source code/docs and letting OpenWiki regenerate.

<!-- OPENWIKI:END -->
