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
| `googlephotos` | `googlephotos.go` | Google Photos Library API (`photoslibrary.googleapis.com/v1`) | OAuth2 (access token in `password` field) | files (albums = directories, media = files) |
| `s3` | `s3.go` | S3 (Wasabi, MinIO, B2, …) | access key / secret key | files |
| `smb` | `smb.go` | SMB2/SMB3 (`go-smb2`) | user/pass | files |
| `sftp` | `sftp.go` | SSH SFTP (`pkg/sftp`) | user/pass (or key) | files |
| `local` | `local.go` | Local filesystem (server-side sandbox) | none (no URL/user/pass) | files only |

---

## 2.1. Local Storage Provider (`local`)

`local` reads and writes files directly from a server-side sandbox directory defined by the
`LOCAL_STORAGE_ROOT` environment variable. It carries **no credentials** (no URL, no username, no
password). All access is rooted at `LOCAL_STORAGE_ROOT`; user-supplied relative paths are joined to the
root and verified to stay within it — `..` traversal and symlinked intermediate directories that escape
the root are rejected. It supports only the `files` resource type; calendars/contacts are not applicable.

The `Local` option appears in the UI **only** when `LOCAL_STORAGE_ROOT` is configured (`local_storage_enabled`
in `GET /api/settings`). `NewProvider("local")` returns an error if the variable is unset or not a
directory. `LOCAL_STORAGE_ROOT` must be set on **both** the api-backend and the worker (the worker
performs the actual file I/O). `local` is exempt from the SSRF egress validation (no network host is
contacted). `GetFileHash` returns a `SHA1:` hash, enabling the standard 3-way hash check.

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

## 3.1. Google Photos (`googlephotos`)

`googlephotos` is a **separate provider** from `google` (it uses its own OAuth client with the narrow
`photoslibrary.readonly.appcreateddata` + `photoslibrary.appendonly` scopes). Albums are mapped to directories (`IsDir = true`) and media items to files
(`resourceType = "files"`); no new `resourceType` values are introduced, so the existing `files`
pipeline (indexer, transfer, integrity) runs unchanged.

- **Auth:** OAuth2 access token passed in the `password` field (like `google`/`dropbox`). Credentials
  env: `GOOGLE_PHOTOS_CLIENT_ID` / `GOOGLE_PHOTOS_CLIENT_SECRET`.
- **Listing:**
  - `/` → all albums as `is_dir=true` entries (`Path = /<albumId>`).
  - `/<albumId>` → media items via `mediaItems:search`; each becomes `Path = /<albumId>/<mediaId>`.
- **Download:** re-fetches a fresh `baseUrl` (short-lived, ~1h) and appends the download suffix for
  original bytes — `=d` for images, `=dv` for `video/*` (videos require `=dv` or the API returns an
  error/scaled thumbnail). The suffix is chosen by the item's `mimeType`.
- **Upload:** raw binary bytes go to `POST /v1/uploads` with `Content-Type: application/octet-stream`,
  `X-Goog-Upload-Protocol: raw`, and `X-Goog-Upload-Content-Type: <mime>`; the response body is the
  **upload token as plain text** (not JSON). It is then referenced in
  `mediaItems:batchCreate` into the album resolved from the path's first segment. The `albumId` +
  `newMediaItems[].simpleMediaItem` payload carries **both** `uploadToken` AND `fileName`; `description`
  is left empty (the API prohibits storing programmatic/auto-generated text there). The album is
  created on demand and **deduplicated** via an in-memory `title ↔ ID` cache so repeated uploads
  into the same album reuse it instead of creating duplicates (the Photos API does **not** dedupe
  albums by title). A Photos→Photos upload whose path carries the source album **id** is mapped onto
  the already-existing target album (keeping its original title) rather than creating a new album
  named after the id.
- **No rename / delete / folder semantics:** `DeleteFile` and `RenameFile` return "not supported".
  `CreateDirectory`/`CreateParentDirectories` create the (target) album.
- **Integrity:** Photos exposes **no content hash**; `GetFileHash` returns empty and the processor
  falls back to size comparison. `InspectResource` populates `Size` via a `HEAD` on `baseUrl`
  (`Content-Length`) so the size fallback is meaningful instead of always `0`.
- **`.tmp` suffix:** the processor's atomic-rename pattern appends `.tmp` to the upload path; the
  provider strips it from both the album segment and the filename, because Photos has no rename
  operation (otherwise the media would be left with a `.tmp` name / orphaned).

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
