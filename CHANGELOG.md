# Changelog

All notable changes to Clumoove will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.0.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [0.16.0] - 2026-08-26

### Added
- **Snapshot Backup & Restore Engine**:
  - Incremental block-level deduplication: files are sliced into 4-MiB SHA-256 blocks, deduplicated against `backup_blocks`, and packed into immutable 64-MiB packs under `.clumoove-backup/<repository-id>/packs/`.
  - Two-phase restore pipeline: Phase 1 computes mandatory read-only conflict previews (`POST /api/backup/{id}/snapshots/{snapshotID}/restore/previews`) with fingerprinting; Phase 2 executes the preview streaming blocks via Range reads or bounded buffers.
  - Retention policies & lifecycle maintenance: automated snapshot pruning (1–365 retention count) and repository integrity checks (`METADATA`, `BUDGETED`, `FULL`) with live SSE progress.
  - Visual cron schedule builder: interactive visual recurrence builder and human-readable cron summaries in the frontend.
  - Dedicated backup dashboards: visual snapshot browser, item catalog search, run histories, and live maintenance diagnostics.
  - Multi-channel completion notifications: full delivery support for Email, Telegram, Discord, Gotify, and ntfy on completed/failed backup runs.
- **Umbrel App Store Packaging**:
  - Official Umbrel App Store package manifests (`umbrel-app.yml`, `docker-compose.yml`) supporting umbrelOS.
  - 1-Click web onboarding via `app_proxy` with dynamic key segregation derived from `APP_PASSWORD`.
  - Configured `backupIgnore` rules for ephemeral Redis queue data.

### Improved & Fixed
- **Database Migrations & Reliability**:
  - Added automatic creation of PostgreSQL trigger function `update_updated_at_column()` in `InitDB()` to guarantee smooth cold-start migrations on fresh databases.
  - Added `backup` and `restore` event kinds to notification check constraints.
  - Handled WebDAV directory self-references and improved stalled backup run recovery.
- **Frontend & UX**:
  - Optimized mobile navigation tab layout and hid native scrollbars on the dashboard.
  - Unified action buttons across migration, sync, and backup dashboards.

## [0.15.0] - 2026-08-19

### Added
- **Cloud File Manager**:
  - Direct cloud browsing, folder creation, and file management across supported providers (Google Drive, OneDrive, Dropbox, Nextcloud, OpenCloud, HiDrive, Seafile, Koofr, MagentaCLOUD, Immich, Local).
  - Dual layout support: switch between a detailed List View and a rich Grid View with responsive cards and persisted view mode preference.
  - In-browser file preview modal supporting Images, PDFs (with bundled worker), Office documents (.docx, .xlsx, legacy .xls via SheetJS), Audio and Video (with autoplay), Plaintext, and file metadata.
  - Fast thumbnail generation and display with rate-limiting backoff/retry and smooth fade-in animations across Google Drive, OneDrive, HiDrive, Seafile, Koofr, and MagentaCLOUD.
  - Multi-file uploads with a floating bottom-center queue overlay, 4 concurrent upload worker slots, and configurable conflict resolution strategies (Overwrite, Rename, Skip).
  - Secure preview and download mechanics via AES-GCM encrypted profile references and single-use Redis tickets without exposing tokens or filesystem paths in URLs.
- **Koofr Cloud Storage Provider**:
  - Native integration for Koofr (`koofr`) cloud storage for migrations, sync passes, and file management using fixed endpoint `app.koofr.net`, application password authentication, and cryptographic MD5 integrity verification.
- **Centralized Iconography & File Icons**:
  - Unified icon hub (`icons.ts`) and MIME-type aware file icons (`FileIcon`) with tailored SVG artwork for all cloud providers (Koofr, MEGA, OneDrive, S3, OpenCloud, Local).

### Improved & Security Hardened
- **Storage & Security Protections**:
  - Google Drive Thumbnail SSRF Hardening: Enforced domain allowlist verification for external Google thumbnail image requests.
  - Local Storage Hardening: Used static descriptor references for descriptor wrapping in Unix-anchored sandbox operations.
  - Decrypted Credential Lifecycle: Enforced idempotent memory zeroing for decrypted passwords and OAuth refresh tokens during profile resolution.
  - Seafile & MagentaCLOUD File Management: Fixed thumbnail API endpoints and mapped root library creation to unsupported operation status.
  - Directory Size Bounds: Capped directory listings in file manager to prevent excessive memory usage.
- **Frontend Architecture & UX**:
  - Lazy-loaded preview worker chunks for Excel and Word documents to optimize bundle size and startup speed.
  - Added race-condition mitigations and navigation guards for rapid directory switching in the file browser.
  - Full English and German internationalization (i18n) for all file manager, preview, and upload dialog features.

### Fixed
- **Navigation & Preview Stability**: Resolved PDF preview worker re-renders, fixed connection settings navigation stability, and resolved download ticket type narrowing in preview dialogs.

## [0.14.0] - 2026-08-12

### Added
- **Umbrel One-Click App Deployment**: Added official Umbrel deployment manifest (`deploy/umbrel/umbrel-app.yml` & `docker-compose.yml`) for seamless 1-click self-hosted installation.
- **Worker Forced Shutdown Support**: Implemented SIGTERM/SIGINT forced shutdown handling for background worker tasks to drain transfers cleanly.

### Improved & Security Hardened
- **Multi-Provider Reliability & Security**:
  - WebDAV / Storage: Hardened WebDAV provider operations and path resolution.
  - Special Providers (Local, HiDrive, Immich, Seafile, MEGA): Resolved TOCTOU symlink risks in Local provider, fixed Immich goroutine leaks on context cancellation, resolved HiDrive partial upload idempotency failures, and corrected MEGA non-atomic rename paths and session decoding.
  - OAuth Providers (Dropbox, Google Drive, OneDrive): Hardened Dropbox emoji-path encoding and credential retention, fixed Google Drive path panic triggers and pagination bugs.
  - Hashing & Verification: Corrected QuickXor hash computation and Dropbox hash classification; improved PROPFIND retry logic.
  - File Protocol Providers (FTP, SFTP, SMB, S3): Hardened authentication error handling for S3/SMB, enforced context deadlines for connection setup, and corrected SFTP hash handling.
  - Security & Egress Validation: Bounded egress IP validation and mitigations for DNS rebinding.
- **Backend Core, Queue & Rate Limiting**:
  - Bandwidth Rate Limiting: Isolated download and upload direction token buckets to eliminate bandwidth cross-throttling.
  - Queue & Sync State Engine: Hardened continuous sync pass state transitions, worker pass generation fencing, and added recovery daemon for stale migration indexing tasks.
  - Security & Auth Hardening: Enforced CSRF origin check on refresh cookie endpoints, pinned TOTP settings and atomic single-use recovery code consumption, hardened credential encryption envelope (`v1:` format), and tightened authentication flows.
  - Observability & Log Redaction: Enhanced JSON `slog` log redaction for sensitive credentials across API and worker processes.
- **Frontend UI & Accessibility**:
  - Modern Technical Typography: Adopted Open Sans font for clean technical text rendering across dashboard views.
  - Accessible UI Primitives: Upgraded `Button`, `ConfirmationDialog`, `Tabs`, `StatusBadge`, and `TransferCard` components with `forwardRef` support, explicit ARIA labels for icon-only buttons, dynamic type assertions, and robust focus management.
  - Form Resilience & State Management: Fixed auth/sync forms, enhanced OAuth re-auth guards, and eliminated re-render loops in file tree modals.
  - Internationalization (i18n): Updated German and English locale catalogs to achieve full UI string parity.

### Fixed
- **Transfer & Sync Stability**: Resolved edge-case file stream cancellations, directory creation race conditions, and lingering worker process locks.
- **UI Error Boundaries**: Prevented UI crashes on disconnected SSE streams and surfaced connection errors directly within the file browser.

## [0.13.0] - 2026-08-09

### Added
- **Mega Storage Provider**: Integrated personal Cloud Drive support (`mega`) for zero-disk streaming file migrations, including root directory uploads, parent path creation, and serialized target operations.
- **Seafile Storage Provider**: Added Seafile provider (`seafile`) supporting personal and library file transfers, shared authentication token caching across tasks, nested directory creation, resumable streaming uploads, and unicode symbol sanitization.
- **OpenCloud Storage Provider**: Added OpenCloud storage provider support (`opencloud`) with seamless WebUI connection form integration.
- **Renewable Session Management**: Implemented renewable login sessions (`/auth/sessions`) with atomic single-transaction token refresh, active session listing, and granular session revocation.
- **Unified Structured `slog` Logging**: Introduced centralized JSON `slog` logging package (`backend/internal/observability`) with request correlation (`X-Request-ID`), level-aware redaction for sensitive fields/URLs, expanded error classification, and frontend dev logger with error boundaries.

### Improved & Security Hardened
- **Redesigned Provider Selection UI**: Upgraded provider selector to a responsive 2-column layout with authentic brand and protocol icons (`react-icons`), 50/50 split width, clean host cards, and refined button actions.
- **Enforced Secure Provider Endpoints**: Required HTTPS for user-configured storage providers (`nextcloud`, `opencloud`, `webdav`, `immich`, `seafile`, custom `s3`) and blocked plaintext HTTP endpoints.
- **SMB Connection Hardening**: Enforced SMB message signing across SMB connections to protect data in transit.
- **Account Enumeration Protection**: Masked login lockout responses with generic authentication failure codes to prevent user enumeration.
- **TOTP Recovery Hardening**: Enforced single-use atomic consumption of TOTP recovery codes before session authorization proceeds.
- **Notification Secret Protection**: Zeroed decrypted notification credentials immediately after delivery attempts.
- **Generation-Fenced Sync Verification**: Implemented database generation fencing for continuous sync checksum passes to enforce single-writer guarantees across worker processes.

### Fixed
- **Seafile & Mega Transfer Reliability**: Fixed root library uploads, parent folder creation race conditions, and long-running streaming upload timeouts for Seafile and Mega providers.
- **Worker & Storage Resilience**: Resolved S3 idle connection leaks, bounded SFTP session setup and operation cancellation, and ensured task-scoped provider connections in worker routines.
- **Atomic Outbox Notifications**: Hardened notification outbox state transitions to ensure atomic completion and delivery reporting.

## [0.12.0] - 2026-08-07

### Added
- **Editable Sync Jobs**: Added support for editing schedule, selected paths, and target configurations for existing continuous synchronization jobs.
- **Bulk Delete for Transfers & Sync Jobs**: Added multi-select bulk deletion capability for migrations and continuous sync jobs in the Web UI.
- **Missing Path Resilience for Sync Jobs**: Optimized continuous sync root path selection and added graceful handling for missing source paths across storage providers.

### Improved & Security Hardened
- **Admin-Managed OAuth Credentials**: Replaced static environment variable OAuth credentials (`*_CLIENT_ID` / `*_CLIENT_SECRET`) with AES-256-GCM encrypted database storage in `instance_oauth_providers`, configurable via the Admin UI.
- **Dynamic & Secure OAuth Redirect URIs**: OAuth redirect URIs are derived dynamically from request context and `X-Forwarded-Proto`, defaulting securely to HTTPS behind trusted proxies.
- **File Timestamp Preservation**: Preserved source file modification timestamps (`mtime`) across supporting target storage providers.
- **Bounded Worker Lock Memory**: Replaced unbounded map growth in processor locks with reference-counted keyed mutexes to reduce worker memory overhead.

### Fixed
- **WebDAV System Conflicts**: Improved WebDAV status 400 handling and conflict resolution during streaming file transfers.
- **Immich Integration**: Streamlined Immich targets to library-only mode and silently skipped non-media files during migration indexing.
- **Sync Browse Credentials**: Fixed deferred password zeroing in `handleBrowseSyncJob` to prevent authorization errors during sync target browsing.
- **UI & Modal Stabilization**: Fixed infinite re-render loops in sync edit mode, restored file tree modal UI parity, and surfaced connection/browse error messages in `FileBrowser`.
- **Linux Build Compatibility**: Replaced platform-specific syscalls with `UtimesNanoAt` for clean Linux cross-compilation.

## [0.11.0] - 2026-08-05

### Added
- **Automatic Failed Task Retries**: Added background task retry engine (`FAILED_RETRYING` task status) that automatically re-executes transient failed migration tasks prior to entering final integrity verification.
- **Immich Target Asset ID & SHA-1 Verification**: Implemented target asset ID tracking and Base64 SHA-1 verification for Immich storage targets.
- **User Last Login Tracking**: Added `last_login_at` timestamp tracking to user authentication flows, admin API endpoints, and Admin UI user tables.

### Improved & Security Hardened
- **Verification Engine Optimization**: Optimized post-migration verification by skipping redundant remote target hash queries for `size_only` target providers (WebDAV, Nextcloud, MagentaCLOUD, FTPS).
- **Single-Writer Verifier Fencing**: Implemented PostgreSQL database leases and generation fencing for migration verification passes to enforce single-writer guarantees across multi-instance worker processes.
- **Cancellation Task Fencing**: Enhanced indexer and task management to prevent new task creation while migration cancellation is actively in progress.

### Fixed
- **Notification Deliveries Parameter Casting**: Fixed PostgreSQL type errors in notification delivery queries by explicitly casting `event_id` and `user_id` parameters to `UUID`.
- **Bulk Task Insert Type Safety**: Fixed parameter type casting for `file_size` and task metadata in bulk task insertion queries.
- **Completion Email Default**: Enabled completion email notifications by default when an instance SMTP server is configured.

## [0.10.0] - 2026-08-03

### Added
- **Centralized Instance SMTP Configuration**: Replaced environment variable SMTP configuration with encrypted database-backed instance SMTP settings, manageable via the Admin UI with test mail delivery functionality and per-user email notification preferences.
- **OneDrive QuickXor Hash Verification**: Implemented non-cryptographic QuickXor checksum computation and streaming verification for Microsoft OneDrive file transfers.

### Improved & Security Hardened
- **Multi-Provider Data Integrity Engine**: Retains target native streaming hashes (including HiDrive `chash` and OneDrive `QuickXor`) when source and target providers use different hash algorithms, enabling precise post-transfer checksum verification.
- **HiDrive Integration**: Enhanced HiDrive provider with native target `chash` validation and automatic URL-decoding of directory listing paths to prevent path mismatch 404 errors.

### Fixed
- **Google Drive Uploads**: Ensured target directory hierarchy is created prior to file streaming to prevent missing parent folder errors.
- **HiDrive Concurrent Upload Directory Race**: Fixed race condition handling when creating parent directories during concurrent file uploads on HiDrive.

### Documentation
- Updated `README.md` and provider documentation for Microsoft OneDrive and HiDrive native content hashing.

## [0.9.0] - 2026-08-02

### Added
- **OneDrive Personal Provider**: Added full support for Microsoft OneDrive Personal accounts (Graph API), including handling of shared folder shortcuts and remote drive resolution.
- **FTPS Storage Provider**: Added secure FTPS provider supporting explicit (`ftp://host:21?tls=explicit`) and implicit (`ftps://host:990`) TLS with DNS-rebinding-safe passive data connections.
- **Branded Responsive Email Templates**: Introduced responsive, beautifully styled HTML email notification templates for migration completions and alerts.
- **Docker & CI/CD Release Pipeline**: Added automated multi-arch Docker release workflows, buildhost optimizations, Node 24 support, and native Go cross-compilation in Dockerfiles.

### Improved & Security Hardened
- **SMTP & Notification Security**: Comprehensive hardening of SMTP delivery including header sanitization, control character stripping, mailbox normalization, and content injection protection.
- **OAuth Callback Security**: Isolated OAuth callback script parameters, target origin validation, untrusted origin reflection prevention, and automatic re-authentication for failed transfers.
- **Queue & Sync Reliability**: Generation fencing for continuous sync passes, staged overwrite upload cleanup, and task claim fencing to prevent job starvation under heavy load.
- **Local Storage Provider**: Improved local directory listing offsets, cursor isolation, and handle-relative operation safety.

### Fixed
- Fixed MagentaCLOUD WebDAV uploads path resolution.
- Fixed translation string replacement sanitization in i18n utility.
- Fixed single-use administrative setup concurrency race.
- Fixed OneDrive shortcut resolution sharing across parallel file transfers.
- Fixed false positive key-derivation security warnings in crypto package.

### Dependencies
- Updated frontend & backend dependencies (Node 24, Vite 6, Vitest 4, heroicons, i18next).

## [0.8.0] - 2026-07-20

### Added
- Initial release of Clumoove multi-cloud migration platform.
