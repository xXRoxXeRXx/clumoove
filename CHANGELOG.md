# Changelog

All notable changes to Clumoove will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.0.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

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
