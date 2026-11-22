# Product

<!-- impeccable:product-schema 1 -->

## Platform

web

## Users

Self-hosting administrators and IT operators. They run their own Clumoove instance and move data between cloud storage providers, NAS systems and servers for themselves or their organization. They are technical, care about control, privacy and data integrity, and open the app to set up, monitor and verify long-running jobs (migrations, syncs, backups). Additional roles exist in the product (per-user accounts, an admin role for instance configuration), but the confirmed primary audience is the self-hosting admin.

## Product Purpose

Clumoove is a self-hosted, multi-user platform for lossless cloud-to-cloud data migration, recurring synchronization and snapshot backup. Success means a job finishes with verified integrity, the operator can see what happened, and credentials never leave their infrastructure.

## Positioning

A durable, account-scoped job platform rather than a command-line tool: migration, sync and backup runs are persisted tasks (PostgreSQL queue, retry, orphan recovery, scheduling, live SSE progress, reports) with per-user isolation, TOTP 2FA, encrypted reusable connection profiles and provider-aware 3-way hash verification. It deliberately covers a curated set of 18 providers instead of rclone's breadth.

## Operating Context

Self-hosted via Docker Compose (Go backend, React frontend, PostgreSQL, Redis, nginx). Jobs run for minutes to days; the operator pauses, resumes, cancels, adjusts thread count and bandwidth, and reviews reports (CSV). Users connect to providers via credentials or OAuth2, browse source and target trees, select paths, choose conflict handling (`SKIP`, `OVERWRITE`, `RENAME`), then execute or schedule.

## Capabilities and Constraints

- Migration Engine, Sync Engine (one-way and two-way), Backup Engine (deduplicated snapshots, two-phase restore), scheduler (one-shot, cron, interval), Connection Profiles, Cloud File Manager, central instance mailer, audit log.
- 18 whitelisted storage providers; resource types beyond files (calendars, contacts) only for Nextcloud and Google Drive; Mega and Immich are files-only (Immich one-time).
- Frontend: React 19, Tailwind 4, `@heroicons/react` for UI icons, `react-icons` only for provider brand logos, semantic `ui-*` tokens in `frontend/src/index.css`.
- All API errors are machine-readable codes translated client-side; `de` and `en` locale keys must stay in strict parity; locale-aware formatting only via `src/utils/format.ts`.
- Credentials are never exposed in URLs or to the client; remote paths are not exposed in file-manager URLs.

## Brand Commitments

Name: Clumoove. Logo: `frontend/public/clumoove_logo.svg`. Self-hosted only (no hosted offering recorded). German-first: `de` is the primary language, `en` secondary.

## Evidence on Hand

Real feature set and provider matrix in README.md and `docs/`. No customer testimonials, case studies, benchmarks or press are recorded; future work must not fabricate them.

## Product Principles

1. Integrity over speed: never claim success that verification has not confirmed.
2. The operator stays in control: every long-running job is observable and interruptible.
3. Privacy by construction: self-hosted, encrypted credentials, strict tenant isolation, no tracking.
4. Say exactly what happened: typed, translated, actionable status and errors instead of raw strings.
5. German-first, bilingual by default: every user-facing string exists in `de` and `en`.

## Accessibility & Inclusion

Target WCAG 2.2 AA. Native semantics first; modals with accessible labels, Escape dismissal, focus trap and focus restoration; menus, tabs and trees fully keyboard-navigable; icon-only buttons carry localized `aria-label` and `title`.
