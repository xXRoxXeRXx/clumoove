---
type: integration
title: Storage-provider integration
description: Storage integrations conform to one mandatory streaming contract, an explicit provider allowlist, and declared atomic-rename and verification capabilities.
tags: [storage, providers, integrations, verification]
verified:
  - by: openwiki/0.7.2
    at: 2026-10-15T10:57:39.355Z
sources:
  - id: openwiki-source-4f649bf2c821f565856d76dd
    resource: repo://backend/internal/storage/factory.go
  - id: openwiki-source-49a4b814540217d6052c95fb
    resource: repo://backend/internal/storage/hash.go
  - id: openwiki-source-b9659cb5f37453d4402b4d86
    resource: repo://backend/internal/storage/provider.go
generated: { by: "codex", at: "2026-10-15T10:57:39.355Z" }
---

# Storage-provider integration

`StorageProvider` is the required interface for every supported backend. It covers connection, listing and inspection, streamed download/upload, directory and file operations, hashing, and close. New providers must also declare `SupportsAtomicRename` and `VerificationMode`; this forces processor behavior to depend on a stated capability rather than guessing from a provider name.

The factory holds the explicit supported-provider registry and constructs the appropriate implementation. It records provider metadata including resource support, authentication characteristics, atomic-rename support, and whether verification can use a cryptographic hash or only independent size checks. Unsupported provider strings are rejected rather than dynamically loaded.

Paths are checked before normalization so traversal cannot escape a configured storage root. Provider endpoint validation and egress clients protect outbound requests; callers must validate user-entered URLs before connecting. Provider errors are typed where their meaning changes retry behavior, including authentication failures, permissions, permanent transfer failures, native duplicates, unsupported resource types, and unavailable hashes.

## Transfer contract

Transfers stream between providers rather than staging whole files in a local cache. The processor can pass a source checksum or provider metadata using scoped contexts, and targets can return a persisted upload receipt for later verification. Hash normalization recognizes common provider labels; ETags are not treated as cryptographic hashes. Providers without comparable hashes use size-oriented verification.

Provider tests and factory tests are the first place to update when adding a backend. Keep the whitelist, factory metadata, implementation, mocks, SSRF handling, capability declarations, and verification behavior in agreement.
