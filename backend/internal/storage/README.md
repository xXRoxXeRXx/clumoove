# Clumoove Storage Provider Architecture

## Overview

The `backend/internal/storage` package defines focused workflow interfaces and a composed `StorageProvider` registration contract and contains concrete client implementations for all supported storage backends (Nextcloud, OpenCloud, WebDAV, Dropbox, Google Drive, OneDrive, HiDrive, SMB, S3, SFTP, FTPS, MagentaCLOUD, Koofr, Local, Immich, Seafile, and MEGA). Koofr uses only the public fixed endpoint, its primary mount, and an email/application-password credential pair; it is files-only, case-insensitive, and provides MD5 hashes for verification.

---

## The `StorageProvider` Interface

Every registered storage backend implements the composed `StorageProvider` contract. Consumers depend on the narrower operations they use; in particular, the file manager accepts `ManagerProvider`:

```go
type StorageProvider interface {
	ManagerProvider
	ProviderLifecycle
	TransferCapabilities
}
```

---

## Architecture Rules & Guidelines

1. **Required Interface**: `StorageProvider` is non-optional. Concrete registered providers implement the composition; workflow test doubles need only their workflow interface.
2. **SSRF Guarding**: The `RequiresEgressValidation` flag on the provider's entry in `providerCatalogueEntries` (`factory.go`) is the source of truth for user-configured providers that require egress validation. Those providers MUST validate URLs using `validateEgressURL` and execute HTTP requests via `NewEgressHTTPClient` or custom transports with `egressDialer`.
3. **Data Integrity & Hashes**:
   - Return native cryptographic hashes (`SHA-1`, `MD5`, `SHA-256`, `QuickXor`, etc.) via `GetFileHash`.
   - Implement `VerificationMode()` returning `VerificationCryptographicHash` when hash verification is available, or `VerificationSizeOnly` otherwise.
4. **Provider Factory Registration**:
   - Add one authoritative `providerRegistration` entry to `providerCatalogueEntries` in `factory.go` supplying validation rules, construction, migration traits, and file-manager capabilities (`ValidProviders` is derived from it in order).
5. **Zero-Disk Retention**: File transfers must be streamed through RAM buffers without writing intermediate data to local disk.
