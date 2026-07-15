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
}
```

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
| `magentacloud` | `magentacloud.go` | WebDAV (fixed endpoint `https://magentacloud.de/remote.php/webdav`) | user/pass | files only |
| `webdav` | `webdav.go` (+ `propfind.go`) | generic WebDAV | user/pass | files |
| `dropbox` | `dropbox.go` | Dropbox API v2 | OAuth2 (access token in `password` field) | files |
| `google` | `google.go` | Drive API v3 / Calendar / People | OAuth2 | files, calendars, contacts |
| `s3` | `s3.go` | S3 (Wasabi, MinIO, B2, …) | access key / secret key | files |
| `smb` | `smb.go` | SMB2/SMB3 (`go-smb2`) | user/pass | files |
| `sftp` | `sftp.go` | SSH SFTP (`pkg/sftp`) | user/pass (or key) | files |

---

## 3. Factory & Validation (`factory.go`)

`NewProvider(ctx, providerType, urlStr, username, password)`:

1. For `nextcloud`/`webdav`, extracts credentials embedded in the URL (`user:pass@host`) and strips them
   from the URL before use (prevents leakage in `url.Error`).
2. For `nextcloud`/`webdav`/`smb`/`sftp`, runs `validateEgressURL` (SSRF guard).
3. Switches on the whitelisted provider type and returns the concrete client. `magentacloud` ignores
   the URL (uses its fixed endpoint). `google` takes the OAuth access token as `password`; `dropbox`
   likewise. Unknown types return `unsupported provider type`.

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
  request URL so TLS SNI/cert validation still targets the real name.

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

`ParseHashString` extracts the algorithm + clean hash from provider hash strings (e.g.
`SHA1:abc123`, `MD5:…`, `SHA256:…`). The processor selects the per-provider hasher accordingly and only
computes a second (target) hasher when algorithms differ (CPU optimization).

---

## 6. Adding a New Provider

1. Create `backend/internal/storage/<name>.go` implementing `StorageProvider` (and `MetadataApplier` if
   applicable).
2. Add the provider value to the whitelist in `api/main.go` (`validProviders` map) **and** the frontend
   provider selector.
3. Register it in `NewProvider` (`factory.go`), including any SSRF egress validation for
   user-supplied hosts.
4. If it is an OAuth provider, wire token refresh in `internal/oauth` and the rotation daemon.
5. Update [Storage Providers](./05-storage-providers.md) and the README provider table.
