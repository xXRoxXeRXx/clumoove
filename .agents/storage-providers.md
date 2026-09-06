# Storage Providers

## Core Interface Requirements
All storage sources and targets must implement `StorageProvider` in `backend/internal/storage/provider.go` and be registered in `factory.go`. The interface requires:

- `SupportsAtomicRename() bool`: Return `true` only if the target supports atomic promotion from `<path>.tmp` to `<path>`. Return `false` for non-atomic targets (e.g. S3, Seafile, Koofr, Immich), which stream directly to the final path.
- `VerificationMode() VerificationMode`:
  - `cryptographic_hash`: Use only when comparable cryptographic hashes are available (e.g., SHA-1, MD5, SHA-256, QuickXor, HiDrive chash).
  - `size_only`: Mandatory for ETag-only, hashless, or non-comparable providers (Nextcloud, WebDAV, MagentaCLOUD, Seafile, FTP, MEGA). Skips `GetFileHash` and verifies existence and size.
  - Immich uses target asset ID + Base64 SHA-1 (`SHA1:<hex>`) with asset-ID size fallback.

## Provider Whitelist
Only accept these 17 explicitly whitelisted providers:
`nextcloud`, `opencloud`, `webdav`, `dropbox`, `google`, `onedrive`, `hidrive`, `smb`, `s3`, `sftp`, `ftp`, `magentacloud`, `koofr`, `local`, `immich`, `seafile`, `mega`.

Never pass unvalidated provider strings to `storage.NewProvider`.

## Provider-Specific Rules

| Provider | Constraints & Implementation Nuances |
|----------|---------------------------------------|
| **Nextcloud / OpenCloud / WebDAV** | HTTPS required. Egress SSRF validated. Uses `size_only` verification (ETags are not cryptographic integrity evidence). |
| **Seafile** | In-memory token cache per server/account (1 in-flight auth per key, respects HTTP 429 `Retry-After`, cleared on 401/403). Download/upload URLs use SSRF-validated, host-pinned client; never leak account token cross-origin. No total request deadline on streaming upload, bounded setup, 5-min post-body timeout. `size_only` verification. |
| **MEGA** | Files-only Cloud Drive. Email/password with forced HTTPS. Stores session ID and master-key material encrypted. Excludes MFA accounts. Rejects ambiguous same-name siblings. `size_only` verification. |
| **Koofr** | Files-only fixed public endpoint (`https://app.koofr.net`). App password with email/username. Primary mount only (no custom endpoints). Normalized MD5 hashes (`cryptographic_hash`). Case-insensitive; sanitizes slash/backslash. `SupportsAtomicRename() == false`. |
| **FTP / FTPS** | Files-only FTPS (`ftp://host:21?tls=explicit` or `ftps://host:990`). Rejects plain unencrypted FTP and URL userinfo. Standard CA verification with SNI. Egress-dialed control/data connections; EPSV preferred, PASV uses control host IP with announced port. `size_only` verification. |
| **SMB** | Requires message signing. Uses SMB3 encryption if server requires it. Recommended only on trusted networks unless encryption is enforced. |
| **OneDrive** | Microsoft Graph personal accounts only. Supports files and shared-folder shortcuts via remote drive/item resolution. Skips Personal Vault (`specialFolder` facet). Calendars/contacts/SharePoint unsupported. Verification: Graph `QuickXor` hash when present. |
| **SFTP** | URL must include trusted `host_key` SHA-256 fingerprint; SSH handshake rejects all others. 15 s socket timeout; cancellation terminates SSH client. |
| **Local** | Zero credentials. Unix only: sandboxed within `LOCAL_STORAGE_ROOT/users/<user-id>` using root-anchored `openat`/`O_NOFOLLOW` traversal to prevent symlink races. Windows mutations unavailable. Files only; requires validated user ID. Only visible in UI if `LOCAL_STORAGE_ROOT` is configured. |
| **Immich** | URL + API key in encrypted password field. Files-only one-time migrations (no sync, backup, or restore). Virtual `/Timeline` and `/Albums` browsing. Native duplicate `SKIP`. Target directory maps to a single album (no nested folders). No atomic rename (`false`). |
| **S3** | HTTPS only (plain HTTP and `insecure=true` are unsupported). Custom endpoints validated for SSRF. `SupportsAtomicRename() == false`. |

## Resource Types & Conflict Resolution
- **Resource Types**: `files`, `calendars`, `contacts`.
  - **Calendars & Contacts**: Always **overwritten** on conflict (dynamic data — `SKIP` would leave stale records).
- **File Conflict Modes**:
  - `SKIP`: Skips transfer (with size-match short-circuit).
  - `OVERWRITE`: Staged to `<file>.tmp` and promoted atomically if `SupportsAtomicRename() == true`; uploaded directly to final path if `false`.
  - `RENAME`: Appends incremental suffix (up to 100 attempts).
- **Sanitization & Collisions**: Filename sanitization (`internal/sanitize`) and case-collision resolution run first for case-insensitive targets.

## Streaming Transfers & Integrity (3-Way Hash Check)
- **Zero Disk Retention**: Streams transfers via RAM buffers; files > 50 MB use chunked upload.
- **Integrity Verification**:
  - `io.TeeReader` computes streaming hash (`SHA1` default; `MD5`, `SHA256`, `DROPBOX`, `QUICKXOR`, or HiDrive `chash`).
  - Target server hash is compared with computed streaming hash. HiDrive source-to-target compares native hashes directly.
  - Decodes URL-escaped API paths before indexing to avoid double-escaped 404s.
  - If target hashes cannot be compared, falls back to size comparison to prevent false corruption errors.
