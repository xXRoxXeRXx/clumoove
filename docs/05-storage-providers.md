# 05 – Storage Providers

All source/target storage is abstracted behind the `StorageProvider` interface
(`backend/internal/storage/provider.go`). New providers must implement that interface and be registered
in `NewProvider` (`factory.go`). Only whitelisted provider strings may reach `NewProvider`.

## Cloud File Manager Capabilities

`ManagerCapabilities` in `storage/file_manager.go` is a separate optional contract from `StorageProvider`; transfer methods and `SupportsAtomicRename()` do not imply a manager operation. File deletion is exposed only through `ManagerDeleter` and sealed manager locators; a root locator is never valid for deletion. Providers retain native deletion semantics, so the UI does not promise recovery. Empty and recursive directory deletion are separately advertised. Immich is flat and exposes only asset deletion through its native asset ID; it never exposes folder deletion.

---

## 1. The `StorageProvider` Interface

```go
type StorageProvider interface {
    Close() error
    Connect(ctx context.Context) (bool, error)
    GetDirectoryListing(ctx, resourceType, dirPath) ([]CloudResource, error)
    InspectResource(ctx, resourceType, path) (CloudResource, error)
    StreamDownload(ctx, resourceType, filePath) (io.ReadCloser, error)
    StreamUpload(ctx, resourceType, filePath, stream, size) error
    StreamUploadChunked(ctx, resourceType, filePath, stream, size, progressChan) error
    FileExists(ctx, resourceType, filePath) (bool, int64, error)
    DeleteFile(ctx, resourceType, filePath) error
    GetFileHash(ctx, resourceType, filePath) (string, error)
    CreateParentDirectories(ctx, resourceType, filePath) error
    CreateDirectory(ctx, resourceType, dirPath) error
    RenameFile(ctx, resourceType, oldPath, newPath) error
    SupportsAtomicRename() bool
    VerificationMode() VerificationMode
}
```

> **Interface contract — required method.** `SupportsAtomicRename()` is a **mandatory** part of the
> interface. Every concrete provider **must** implement it, or the package will fail to compile with
> `does not implement storage.StorageProvider (missing method SupportsAtomicRename)`. There is no
> default; the compiler enforces it for *all* implementers, including test mocks. When adding a new
> provider, add this method alongside the others.
>
> - Return `true` only when the provider can atomically promote a staged `<path>.tmp` upload to `<path>`.
> - Return `false` when that promotion is not atomic (including S3, Seafile, and Koofr) or unsupported (Immich).
>   The processor then uploads directly to the final path. `false` does not imply that the provider lacks
>   every rename or delete operation.

`VerificationMode()` is also mandatory. Return `cryptographic_hash` only when
`GetFileHash` provides a target hash that can be compared with a source or
streaming hash; return `size_only` when the target can only be checked for
existence and size. The verifier never calls `GetFileHash` for `size_only`
targets. In particular, Nextcloud, generic WebDAV, and MagentaCLOUD use
`size_only`: their ETags are not cryptographic integrity evidence.

`none` is reserved for targets that cannot be independently checked at all.
The verifier performs no provider calls and does not mutate the task in that
mode, so no current production provider returns it; adding one requires an
explicit job-finalization policy.

Optional capability interface:

```go
type MetadataApplier interface {
    ApplyMetadata(ctx, resourceType, filePath string, meta FileMetadata) error
}
```

When a target client implements `MetadataApplier`, the processor applies file metadata (modification
time, description, tags, etc.) after a successful upload.

### Supporting types

- `CloudResource` — `Path`, `Name`, `Size`, `IsDir`, `Hash`, `LastModified`, `Metadata`.
- `FileMetadata` — `ModifiedTime`, `Description`, `Tags`, `Starred`, `CustomProps`.
- `ErrAuth` — sentinel returned (wrapped) on HTTP 401 so the processor can detect auth failures via
  `errors.Is`.
- `ErrDuplicateUID` — SabreDAV duplicate UID (calendars); treated as `SKIP`.

---

## 2. Supported Providers

| Provider | File (`storage/*.go`) | Protocol | Auth | Resource types |
| :------- | :-------------------- | :------- | :--- | :------------- |
| `nextcloud` | `nextcloud.go` | WebDAV + OC extensions | user/pass | files, calendars (CalDAV), contacts (CardDAV) |
| `opencloud` | `opencloud.go` | WebDAV (`dav/spaces/`) + TUS 1.0.0 | user/pass or Bearer token | files only |
| `magentacloud` | `magentacloud.go` | WebDAV (fixed endpoint `https://magentacloud.de/remote.php/webdav`) | user/pass | files only |
| `koofr` | `koofr.go` | Koofr API (fixed public endpoint `https://app.koofr.net`) | email/username + application password | files only |
| `webdav` | `webdav.go` (+ `propfind.go`) | generic WebDAV | user/pass | files |
| `dropbox` | `dropbox.go` | Dropbox API v2 | OAuth2 (access token in `password` field) | files |
| `google` | `google.go` | Drive API v3 / Calendar / People | OAuth2 | files, calendars, contacts |
| `onedrive` | `onedrive.go` | Microsoft Graph personal OneDrive | OAuth2 (access token in `password` field) | files only |
| `hidrive` | `hidrive.go` | Strato HiDrive REST API v2.1 | OAuth2 | files only |
| `s3` | `s3.go` | S3 (Wasabi, MinIO, B2, …) | access key / secret key | files |
| `smb` | `smb.go` | SMB2/SMB3 (`go-smb2`) | user/pass | files |
| `sftp` | `sftp.go` | SSH SFTP (`pkg/sftp`) | user/pass (or key), trusted SHA-256 host-key fingerprint | files |
| `ftp` | `ftp.go` | FTPS: explicit or implicit TLS | user/pass | files |
| `local` | `local.go` | Local filesystem (server-side sandbox) | none (no URL/user/pass) | files only |
| `immich` | `immich.go` | Immich stable v2 API | server URL + API key in encrypted password field | files only, one-time migrations |
| `seafile` | `seafile.go` | Seafile Web API v2.1 | server URL + user/pass (or Personal Access Token) | files only |
| `mega` | `mega.go` | MEGA Cloud Drive API over HTTPS | email/password; encrypted reusable session | files only |

SMB clients require SMB message signing, so an SMB server that cannot negotiate signing is rejected. The
current `go-smb2` API enables SMB3 encryption when the server or mounted share requires it, but does not expose
a client-side option to require encryption; use SMB only on trusted networks unless the server/share enforces
SMB3 encryption.

For Seafile username/password connections, workers share an in-memory account-token cache per server/account. A single in-flight authentication is allowed for each cache key; HTTP `429 Too Many Requests` honors the server's `Retry-After` value before another authentication is attempted. Tokens are never persisted by this cache and are removed when the server rejects them with `401` or `403`. Server-issued download/upload links use their own SSRF-validated, host-pinned client so a Seafile CDN or object-storage host can differ from the configured server; the account token is never sent to a cross-origin link. Seafile streaming uploads retain redirect rejection and have no total request deadline; connection setup remains bounded and a response must arrive within five minutes after the upload body is sent.

### Verification capabilities

| Provider | Mode | Basis |
| :------- | :--- | :---- |
| Dropbox | `cryptographic_hash` | Dropbox content hash |
| Google Drive | `cryptographic_hash` | MD5 |
| Koofr | `cryptographic_hash` | API `hash`, normalized as `MD5:<lowercase-hex>` |
| OneDrive | `cryptographic_hash` | QuickXor |
| HiDrive | `cryptographic_hash` | HiDrive `chash` |
| Local | `cryptographic_hash` | SHA-1 |
| Seafile | `size_only` | Its API exposes Seafile object IDs, not a comparable SHA-1 hash of the complete file content |
| Nextcloud, OpenCloud, MagentaCLOUD, WebDAV | `size_only` | ETags are not integrity evidence (OpenCloud parses dynamic `oc:checksums` header when provided) |
| S3 | `size_only` | Multipart ETags are not comparable hashes |
| SMB, SFTP, FTPS | `size_only` | No portable target-hash API |
| Immich | `cryptographic_hash` | Asset `checksum` (Base64 SHA-1, normalized to `SHA1:<lowercase-hex>`) |
| MEGA | `size_only` | No comparable target-hash API |

Koofr uses only `https://app.koofr.net`; self-hosted, white-label, compatible-service endpoints, and mount selection are intentionally unsupported. `Connect` resolves the account's primary mount. It rejects redirects, streams multipart uploads without disk buffering, treats names case-insensitively, sanitizes slash and backslash in target names, and uploads directly to the final name for `OVERWRITE` because move is not documented as atomic.

Immich provider constraints: Immich is strictly a files-only, one-time migration target or source. It cannot be used as a sync source or target, nor as a backup source/target or restore destination.

---

### SFTP host identity

SFTP endpoints must include a URL-encoded `host_key` query parameter containing
the server's trusted SHA-256 SSH fingerprint, in the `SHA256:<base64>` format
emitted by `ssh-keygen`. The fingerprint must be obtained through a trusted
administrative channel; it is pinned when the provider is created, and the SSH
handshake rejects any server key that does not match. Connections without a valid
fingerprint are rejected before authentication, preventing a network attacker
from impersonating the SFTP server or receiving password authentication
credentials.

SFTP connection setup derives its 15-second maximum from the caller's context
and sets that bound as a socket deadline for both the SSH handshake and SFTP
subsystem startup; the deadline is cleared only after the session is ready.
Because `pkg/sftp` has no context-aware request API, every session operation
watches that context and closes the underlying SSH client on cancellation or a
deadline. Operations are serialized while a download stream is open, so a
cancelled stream cannot disrupt another operation; callers receive `ctx.Err()`
and the next operation establishes a fresh session.

---

## 2.1. FTPS Provider (`ftp`)

`ftp` supports files only and always uses TLS-protected FTP. It can be a source or target for migrations,
sync jobs, and connection profiles. The only accepted endpoint forms are:

- Explicit FTPS: `ftp://host:21?tls=explicit`. The server must successfully upgrade the control
  connection with `AUTH TLS`, `PBSZ 0`, and `PROT P`.
- Implicit FTPS: `ftps://host:990`. TLS starts before the FTP control handshake.

Plain FTP is not supported: `ftp://` URLs without exactly `tls=explicit` are rejected. URLs must not carry
userinfo; the username and password are persisted in the existing encrypted credential fields. URL paths
do not define an FTP root. Unknown, duplicate, or conflicting query parameters, fragments, and invalid
ports are rejected.

TLS uses the system CA trust store and validates the configured hostname through SNI. TLS 1.2 is the
minimum version. There is no certificate-validation bypass, custom CA, client certificate, active FTP, or
cleartext fallback.

The FTP control connection and every passive data connection use the DNS-rebinding-safe egress dialer.
EPSV is preferred. If the provider falls back to PASV, it uses only the port returned by the server and
always opens the data connection to the already validated control host; it ignores the PASV response's
advertised host or IP address. This prevents a malicious or misconfigured server from redirecting a data
connection to a different egress destination.

FTP does not provide portable file checksums, so the processor's integrity verification falls back to a
size comparison. FTPS supports server-side rename within the target directory and therefore supports the
temporary-upload then rename overwrite pattern.

### FTPS deployment requirements

The API and worker containers need outbound TCP access to the FTPS control port (21 for explicit FTPS or
990 for implicit FTPS) and to the passive data-port range configured on the specific FTPS server. No
inbound Docker port publication is required for FTPS. A server that requires a distinct passive data host
is intentionally unsupported because data channels are pinned to the validated control host.

---

## 3. Range-Read & Pack Retrieval Requirements (Backup & Restore)

Storage providers serving as backup targets (where pack files `.clumoove-backup/<repo-id>/packs/*.pack` are stored) must support efficient reading of stored pack blocks. Providers that implement `storage.RangeDownloader` fetch individual 4 MiB blocks directly without downloading the entire 64 MiB pack. The current implementations are S3, WebDAV (including Nextcloud and OpenCloud), HiDrive, Dropbox, Google, Koofr, Local, OneDrive, Seafile, SFTP, and SMB. Providers without `RangeDownloader` use bounded in-memory pack readers (`MAX_RESTORE_PACK_READERS`) to cache and slice blocks locally during restore runs.

---

## 4. SSRF Protection (`ssrf.go`)

`validateEgressURL` / `ValidateEgressHost` reject URLs/hosts that resolve to blocked addresses, defending
the API against Server-Side Request Forgery through the connect/browse endpoints.

- **Always blocked:** loopback (`127.0.0.0/8`, `::1`) and link-local (`169.254.0.0/16`, including the
  cloud metadata endpoint `169.254.169.254`).
- **Blocked only when `MIGRATION_BLOCK_PRIVATE=1`/`true`:** RFC1918/ULA private ranges. By default
  private ranges are **permitted** because the tool exists to migrate between self-hosted / internal
  servers.
- **DNS-rebinding (TOCTOU) defense:** validation happens both at construction time (resolve + inspect
  every IP) **and** per-connection inside `egressDialer`'s `DialContext`, which re-resolves the hostname
  and dials only a validated IP immediately before connecting. The original hostname stays in the
  request URL so TLS SNI/cert validation still targets the real name. The dialer is bound to the
  configured hostname or literal IP and rejects attempts to select a different host.
- **Redirects:** user-configured provider, S3, and notification HTTP clients do not follow redirects.
  Configure the canonical HTTPS endpoint and S3 regional endpoint directly; HTTP-to-HTTPS and S3
  regional redirect responses are returned to the caller rather than followed.
- **FTPS data channels:** `ftp` validates the configured control host for every control and passive data
  connection. EPSV is preferred; PASV never supplies a new host, only the passive data port.

### HTTPS requirement

Nextcloud, OpenCloud, generic WebDAV, Immich, Seafile, and custom S3 endpoints require HTTPS.
Plaintext HTTP endpoints and the former S3 `insecure=true` option are rejected.

---

## 5. Hash Parsing

`ParseHashString` in `backend/internal/storage/hash.go` extracts the algorithm + clean hash from provider hash strings (e.g.
`SHA1:abc123`, `MD5:…`, `SHA256:…`, `HIDRIVE:…`). The processor selects the per-provider hasher accordingly and only
computes a second (target) hasher when algorithms differ (CPU optimization).

---

## 6. Adding a New Provider

1. Create `backend/internal/storage/<name>.go` implementing `StorageProvider` (and `MetadataApplier` if
   applicable).
2. **Implement `SupportsAtomicRename() bool` and `VerificationMode() VerificationMode`.** These are required interface methods (no default). Forgetting
   it produces a compile error `does not implement storage.StorageProvider (missing method
   SupportsAtomicRename)` for *every* implementer, including test mocks — so add it together with the other
   methods. Return `true` when the provider supports an atomic "upload to `<path>.tmp` then rename"
   overwrite (standard file providers), or `false` when it cannot rename/delete (e.g. Immich).
   Return `cryptographic_hash` only for a comparable target hash; ETag-only and hashless targets must
   return `size_only` so verification makes only the required existence/size query.
3. Add the provider value to the whitelist in `internal/storage/factory.go` (`ValidProviders`) **and** the frontend
   provider selector.
4. Register it in `NewProvider` (`factory.go`), including any SSRF egress validation for
   user-supplied hosts.
5. If it is an OAuth provider, wire token refresh in `internal/oauth` and the rotation daemon.
6. Update [Storage Providers](./05-storage-providers.md) and the README provider table.
