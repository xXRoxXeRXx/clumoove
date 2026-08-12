# Clumoove Storage Provider Architecture

## Overview

The `backend/internal/storage` package defines the unified `StorageProvider` interface and contains concrete client implementations for all supported storage backends (Nextcloud, OpenCloud, WebDAV, Dropbox, Google Drive, OneDrive, HiDrive, SMB, S3, SFTP, FTPS, MagentaCLOUD, Local, Immich, Seafile, and MEGA).

---

## The `StorageProvider` Interface

Every storage backend MUST implement all methods of `StorageProvider` defined in `provider.go`:

```go
type StorageProvider interface {
	Close() error
	Connect(ctx context.Context) (bool, error)
	GetDirectoryListing(ctx context.Context, resourceType, dirPath string) ([]CloudResource, error)
	InspectResource(ctx context.Context, resourceType, path string) (CloudResource, error)
	StreamDownload(ctx context.Context, resourceType, filePath string) (io.ReadCloser, error)
	StreamUpload(ctx context.Context, resourceType, filePath string, stream io.Reader, size int64) error
	StreamUploadChunked(ctx context.Context, resourceType, filePath string, stream io.Reader, size int64, progressChan chan<- int64) error
	FileExists(ctx context.Context, resourceType, filePath string) (bool, int64, error)
	DeleteFile(ctx context.Context, resourceType, filePath string) error
	GetFileHash(ctx context.Context, resourceType, filePath string) (string, error)
	CreateParentDirectories(ctx context.Context, resourceType, filePath string) error
	CreateDirectory(ctx context.Context, resourceType, dirPath string) error
	RenameFile(ctx context.Context, resourceType, oldPath, newPath string) error
	SupportsAtomicRename() bool
	VerificationMode() VerificationMode
}
```

---

## Architecture Rules & Guidelines

1. **Required Interface**: `StorageProvider` is non-optional. All methods must be implemented by concrete provider structs and unit test mocks.
2. **SSRF Guarding**: `providerRegistry.RequiresEgressValidation` is the source of truth for user-configured providers that require egress validation. Those providers MUST validate URLs using `validateEgressURL` and execute HTTP requests via `NewEgressHTTPClient` or custom transports with `egressDialer`.
3. **Data Integrity & Hashes**:
   - Return native cryptographic hashes (`SHA-1`, `MD5`, `SHA-256`, `QuickXor`, etc.) via `GetFileHash`.
   - Implement `VerificationMode()` returning `VerificationCryptographicHash` when hash verification is available, or `VerificationSizeOnly` otherwise.
4. **Provider Factory Registration**:
   - Add static metadata to `providerRegistry` (the validation source of truth) and keep `ValidProviders` in parity for API/UI ordering.
   - Add switch cases in `NewProvider`.
5. **Zero-Disk Retention**: File transfers must be streamed through RAM buffers without writing intermediate data to local disk.
