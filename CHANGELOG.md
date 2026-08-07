# Changelog

All notable changes to Clumoove will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.0.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

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
