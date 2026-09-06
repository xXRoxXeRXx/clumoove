# Security & Authentication

## Cryptography & Key Segregation
- **Dual Secret Keys**:
  - `ENCRYPTION_SECRET_KEY`: Used exclusively for AES-256-GCM encryption/decryption (`crypto.EncryptWithDomain` / `crypto.DecryptBytesWithDomain`). Persisted fields use stable AAD domains. Versioned ciphertext is prefixed with `v1:`. Key is derived via SHA-256 in `crypto.deriveKey`.
  - `JWT_SECRET_KEY`: Used exclusively for signing JWT access tokens (minimum 32 bytes, must differ from `ENCRYPTION_SECRET_KEY`).
  - **Startup Refusal**: The API gateway refuses to start if either secret is missing, if `JWT_SECRET_KEY == ENCRYPTION_SECRET_KEY`, or if `JWT_SECRET_KEY` is < 32 bytes.
- **Credential Lifecycle**: Never pass plaintext credentials to background goroutines. Fetch from DB by `MigrationID` and decrypt at the last moment using domain-bound decryption. Callers must clear the decrypted byte buffer immediately after use.

## Authentication, Tokens & Sessions
- **JWT Rules**: Issuer must be `clumoove-api`. Allows 30 seconds of clock skew. User ID is extracted exclusively from validated claims (`auth.ClaimsKey`).
- **Passwords**: Enforce 12-character minimum and bcrypt's 72-byte maximum limit across creation, reset, and change endpoints.
- **OAuth2 Token Handling**:
  - Encrypted on migration/sync records (`source_refresh_token_encrypted`, `target_refresh_token_encrypted`).
  - Rotated by `RunOAuthRotationDaemon` before expiration. Transient refresh failures are retried; only rejected credentials terminate jobs. Token updates are atomic against previous ciphertext. Responses capped at 1 MiB; upstream provider errors are never forwarded.
- **Refresh Tokens & Sessions**:
  - Stored as hashes only, mapped to public session IDs and capped User-Agent strings.
  - Revocation by ID does not disclose whether a cross-account session exists.
  - Stored in HTTP-only `Secure; SameSite=None` cookies.
  - Refresh and logout endpoints require an explicit origin match against `allowedOrigins` whitelist to prevent CSRF.

## Egress Validation & SSRF Protection
All user-configured endpoints (`nextcloud`, `opencloud`, `webdav`, `smb`, `sftp`, `ftp`, `immich`, `seafile`, custom S3) are validated via `storage/ssrf.go`:
- **Blocked Ranges**: Loopback (`127.0.0.0/8`, `::1`) and link-local/cloud metadata (`169.254.0.0/16`, `169.254.169.254`) are unconditionally blocked.
- **Private Networks**: RFC1918/ULA ranges are blocked when `MIGRATION_BLOCK_PRIVATE=1` (permitted by default for homelab/internal servers).
- **DNS Rebinding Defense (TOCTOU)**: Addresses are re-resolved and re-validated inside `egressDialer.DialContext` immediately before connection; the real hostname is preserved for TLS SNI and certificate verification.
- **Redirects**: HTTP client does not follow redirects.
- **HTTPS Enforcement**: Plaintext HTTP is rejected for user providers.

## Network & Redis Security
- **Redis Security**: Requires non-empty, non-default password in `REDIS_URL` or `REDIS_PASSWORD`. Rejects known development defaults (`redis_secret`, `dev_redis_secure_pass_999`). Redis must not be exposed to the host network.
- **CORS Whitelist**: Never reflect incoming `Origin` headers or permit wildcards with credentials. Validated against static localhost variants and `CORS_ALLOWED_ORIGIN`.

## API Guardrails, Rate Limiting & Lockouts
- **Request Body Limits**: Enforced via `http.MaxBytesReader`:
  - Standard JSON: 1 MiB.
  - Avatar JSON: 3 MiB.
  - Auth / TOTP JSON: 64 KiB.
  - Oversized payloads return `INVALID_BODY`.
- **Redis Rate Limits (per IP & Group)**:
  - Login: 10/min.
  - Registration: 5 per 5 min.
  - Connect / Browse / Mkdir / Session management: 30/min.
  - Migration / Sync create/start: 10/min.
  - TOTP: 10/min.
  - SSE Stream: 60/min (max 10 concurrent streams per user).
  - Respects `X-Forwarded-For` only when `TRUSTED_PROXY` is configured.
- **Account Lockouts**: 5 failed logins or 5 failed TOTP attempts result in an atomic 15-minute lockout in DB.
- **Security Headers**: `securityHeadersMiddleware` sets `X-Content-Type-Options: nosniff`, `X-Frame-Options: DENY`, `Referrer-Policy: no-referrer`, and strict CSP (`default-src 'none'; frame-ancestors 'none'`). HSTS applied over HTTPS.
- **Server Timeouts**: Read 30 s, Write 60 s, Idle 120 s. Long-running SSE endpoints reset their write deadline using `http.ResponseController`.

## Audit & Logging
- **Structured Logging (`slog`)**: Emit JSON to stdout. Never log passwords, tokens, request/response bodies, or URL userinfo. File paths are personal metadata and must not appear in normal INFO logs (DEBUG only).
- **Client Error Redaction**: Never return raw `err.Error()` or English messages to API clients. Return structured `APIErrorCode` values only.
- **Audit Log**: Best-effort asynchronous audit logging for authentication, lifecycle, 2FA, and settings changes. Client IPs are sanitized against CR-LF injection (CWE-117).

## 2FA / TOTP & Admin Bootstrap
- **TOTP**: Verification required to enable; backup codes are single-use and consumed upon use.
- **Admin Setup**: Initial setup is allowed only when `COUNT(*) == 0` users exist (`POST /api/auth/setup-admin`). Permanently locked thereafter with `403 SETUP_ALREADY_COMPLETED`.
- **User Suspension**: Pauses active migrations and syncs, terminates running transfers, and deactivates schedules.
