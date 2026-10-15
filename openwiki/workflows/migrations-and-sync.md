---
type: workflow
title: Migrations and synchronization
description: Migration indexing creates durable transfer work, and sync passes calculate directional deltas before sharing the processor and verification mechanisms.
tags: [migration, sync, indexing, verification]
verified:
  - by: openwiki/0.7.2
    at: 2026-10-15T10:57:39.355Z
sources:
  - id: openwiki-source-ceaa31f4fb62eb4563b06331
    resource: repo://backend/internal/indexer/indexer.go
  - id: openwiki-source-38fac1d7c5711b06796f51f0
    resource: repo://backend/internal/processor/processor.go
  - id: openwiki-source-ca98b0b9ead46781acdbad98
    resource: repo://backend/internal/sync/engine.go
generated: { by: "codex", at: "2026-10-15T10:57:39.355Z" }
---

# Migrations and synchronization

A migration begins with validated source and target configuration, selected paths, a conflict strategy, and tenant ownership. The indexer walks selected source trees and records transfer tasks with the source path, size, resource type, and available hash. It refreshes an expiring source OAuth token when necessary and persists token updates with conflict handling.

Workers use the processor to claim tasks, obtain provider clients, stream each object, update progress, and finalize the durable task outcome. The processor divides worker capacity between transfers and checksum verification, limits provider usage, serializes conflicting target-file work, and records connection-loss state for recovery. It supports directory work, path/case-collision checks, and provider-specific semantics.

Sync jobs run as passes protected by PostgreSQL advisory locks. The sync engine compares source, target, and stored sync state to derive operations. Two-way work distinguishes changed sides, deletions, and conflicts; `conflict_copy` creates a non-destructive renamed copy where configured. After transfer work completes, the engine determines which tasks need independent checksum verification and transitions the pass through verification before completion.

Migration and sync handlers expose creation, status, control, progress streams, and retryable configuration updates. Conflict strategies and verification guarantees are bounded by the paired providers' declared capabilities; see [Storage-provider integration](../integrations/storage-providers.md).
