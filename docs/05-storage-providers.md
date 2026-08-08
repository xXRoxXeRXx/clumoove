# 05 – Storage Providers

All source/target storage is abstracted behind the `StorageProvider` interface
(`backend/internal/storage/provider.go`). New providers must implement that interface and be registered
in `NewProvider` (`factory.go`). Only whitelisted provider strings may reach `NewProvider`.

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
> - Return `true` for providers that support an atomic "upload to `<path>.tmp` then rename to `<path>`"
>   overwrite pattern (all standard file providers: Nextcloud/WebDAV, S3, SMB, SFTP, FTPS, Dropbox, Google
>   Drive, Local).
> - Return `false` for providers that **cannot** rename or delete (for example, `immich`, which writes
>   an asset directly and relies on its native duplicate handling). The processor then skips the
>   temp-file + rename step entirely.

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

### Verification capabilities

| Provider | Mode | Basis |
| :------- | :--- | :---- |
| Dropbox | `cryptographic_hash` | Dropbox content hash |
| Google Drive | `cryptographic_hash` | MD5 |
| OneDrive | `cryptographic_hash` | QuickXor |
| HiDrive | `cryptographic_hash` | HiDrive `chash` |
| Local | `cryptographic_hash` | SHA-256 |
| Seafile | `cryptographic_hash` | SHA-1 |
| Nextcloud, OpenCloud, MagentaCLOUD, WebDAV | `size_only` | ETags are not integrity evidence (OpenCloud parses dynamic `oc:checksums` header when provided) |
| S3 | `size_only` | Multipart ETags are not comparable hashes |
| SMB, SFTP, FTPS | `size_only` | No portable target-hash API |
| Immich | `cryptographic_hash` | Asset `checksum` (Base64 SHA-1, normalized to `SHA1:<lowercase-hex>`) |

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

## 2.2. Local Storage Provider (`local`)

`local` reads and writes files from a server-side, tenant-isolated sandbox defined by the
`LOCAL_STORAGE_ROOT` environment variable. It carries **no credentials** (no URL, no username, no
password). Each provider instance is rooted at `LOCAL_STORAGE_ROOT/users/<user-id>`, where the user ID is
derived server-side from authenticated JWT claims (API paths) or the persisted migration/sync owner
(background paths). It is never supplied by the request or profile. On Unix-like hosts, descriptor-relative,
component-by-component `openat` with `O_NOFOLLOW` anchors both the configured root and every local
operation; this rejects `..` traversal and symlink replacement races without ever re-opening an
attacker-controlled path. Local-provider mutations are intentionally unavailable on Windows until an equivalent handle-relative
implementation exists. Creating a local provider without a valid user scope fails. It supports only the
`files` resource type; calendars/contacts are not applicable.

## 2.3. HiDrive Provider (`hidrive`)

HiDrive uses its fixed REST v2.1 endpoint and OAuth2 bearer tokens. It supports files only. API response
names and paths are URL-escaped; `hidrive.go` decodes them before they reach the indexer, so a name such as
`deprecated%2Bbuild.9` is subsequently requested as `deprecated+build.9`, never double-escaped to `%252B`.

HiDrive's `chash` is a provider-specific hierarchical content hash, not SHA-1 despite its 20-byte length.
`hidrivehash.go` calculates it while streaming to a HiDrive target: SHA-1 is calculated for each 4096-byte
block (with zero padding for the final block); up to 256 position-bound child hashes are SHA-1 transformed
and added modulo 2^160, recursively. All-zero blocks are represented as empty slots, as specified by
HiDrive. The generated `HIDRIVE:<chash>` is compared with the target's server-side `chash` after upload.
This applies to migrations and sync passes. HiDrive-to-HiDrive transfers compare the native source and
target `chash` values directly. For a non-HiDrive source, the worker-generated HiDrive hash is used instead
of falling back to a size-only comparison.

QuickXor hashes are base64 values and remain case-sensitive when normalised for comparison.

The `Local` option appears in the UI **only** when `LOCAL_STORAGE_ROOT` is configured (`local_storage_enabled`
in `GET /api/settings`). `NewProvider("local")` returns an error if the variable is unset or not a
directory. `LOCAL_STORAGE_ROOT` must be set on **both** the api-backend and the worker (the worker
performs the actual file I/O). `local` is exempt from the SSRF egress validation (no network host is
contacted). `GetFileHash` returns a `SHA1:` hash, enabling the standard 3-way hash check.

## 3. Factory & Validation (`factory.go`)

### Immich

Immich uses a server URL and API key sent as `x-api-key`; the key is stored in the encrypted password field and is never logged. No username is needed. The supplied URL may include the `/api` suffix; the provider normalizes it to the API base URL. It uses the stable v2 endpoint subset for API-key validation, asset search/download/upload. Immich is a flat photo library: both source browsing and the upload target present the library root (`/`) as a flat list of asset IDs (no `/Timeline`, no `/Albums`, no album assignment). Asset IDs, rather than filenames, identify source assets; the original filename is retained in task metadata so the real file name is preserved on download.

Immich is files-only and supports one-time migrations only: calendars, contacts, and sync jobs are rejected. An Immich target requires the native-duplicate `SKIP` conflict strategy; overwrite, rename, filename deletion, and atomic rename are unsupported. Uploaded assets land directly in the Immich library and are never assigned to an album. Directory/album creation (`CreateDirectory`) is a no-op. Only supported image, video, and RAW extensions are indexed for an Immich target; rejected files are recorded as indexing errors.

New Immich uploads persist the returned target asset ID in task metadata. Verification uses only that ID with `GET /assets/{id}`: its Base64 SHA-1 `checksum` is normalized to `SHA1:<lowercase-hex>`, while `exifInfo.fileSizeInByte` is the fallback when no checksum is available. ETags are never integrity evidence. Historical completed tasks without a persisted target asset ID are deliberately left unverified rather than guessed from an album or filename; retransfer is required for a trustworthy check.

`NewProvider(ctx, providerType, urlStr, username, password)`:

1. For `nextcloud`/`webdav`, extracts credentials embedded in the URL (`user:pass@host`) and strips them
   from the URL before use (prevents leakage in `url.Error`).
2. For `nextcloud`/`webdav`/`smb`/`sftp`/`ftp`/`immich`, runs `validateEgressURL` (SSRF guard).
3. Switches on the whitelisted provider type and returns the concrete client. `magentacloud` ignores
   the URL (uses its fixed endpoint). `google`, `dropbox`, `onedrive`, and `hidrive` take the OAuth access token as `password`. Unknown types return `unsupported provider type`.

### OneDrive Personal

`onedrive` uses fixed Microsoft Graph endpoints and the `consumers` OAuth authority, so it supports personal accounts and files only. Shared folders exposed as shortcuts in the user's root are supported: Clumoove resolves the shortcut's remote drive and item identity before listing, inspecting, or downloading descendants. Personal Vault is identified by Graph's `specialFolder.name = vault` facet and is excluded from selection/indexing because its interactive unlock cannot be performed by a background OAuth job. SharePoint, organizational accounts, calendars, and contacts are intentionally excluded. Graph `eTag` values are retained for sync change detection. When Graph exposes a file's non-cryptographic QuickXor hash, Clumoove calculates the same algorithm while streaming and uses it for provider-specific verification; unavailable hashes still fall back to size verification. Target filenames follow OneDrive's Windows-style forbidden-character, reserved-name, trailing-punctuation, 255-character segment, 400-character path, and case-insensitive rules.

Provider URL normalization: `normalizeProviderURL` substitutes the constant MagentaCLOUD URL when the
provider is `magentacloud` (the frontend sends an empty URL).

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

### S3-specific SSRF

`insecure=true` S3 endpoints permit only loopback, `*.local`/`localhost`, and RFC1918/ULA
(private) hosts, evaluated **directly without DNS resolution** to prevent DNS-rebinding SSRF
(see `allowInsecureEgress` in `ssrf.go`, the single source of truth also used by the S3
provider). Link-local addresses — notably the cloud metadata endpoint `169.254.169.254` — are
always rejected, and RFC1918/ULA ranges are additionally rejected when `MIGRATION_BLOCK_PRIVATE=1`.
The actual TCP dial re-resolves and re-validates the address via `egressDialer`, so the
construction-time check and the per-connection check agree.

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
