---
type: workflow
title: Backups and restore
description: Backup jobs create durable snapshot repositories and restoration proceeds through an owned preview before a worker reconstructs selected items.
tags: [backup, restore, snapshots, verification]
verified:
  - by: openwiki/0.7.2
    at: 2026-10-15T10:57:39.355Z
sources:
  - id: openwiki-source-2d569a338f15d601591eeca5
    resource: repo://backend/cmd/api/backup_handlers.go
  - id: openwiki-source-2e0edab4820bd289b126616c
    resource: repo://backend/internal/backup/coordinator_test.go
  - id: openwiki-source-2106552df997de92fb587d77
    resource: repo://backend/internal/restore/coordinator.go
generated: { by: "codex", at: "2026-10-15T10:57:39.355Z" }
---

# Backups and restore

Backup jobs are durable, user-owned configurations that target a repository on a supported storage provider. API handlers create jobs, start manual passes, expose runs, snapshots, verification activity, and streams, and enforce backup ownership. The database grants a backup pass claim before a coordinator runs it, preventing overlapping work.

The backup coordinator persists repository metadata, snapshot catalogs, packs, blocks, and integrity state. Pack writer capacity is limited per worker. A snapshot catalog only accepts reconstructable files: the tests cover rejecting missing pack blocks, non-ready packs, and invalid block sequences. Backup verification is a separate queued maintenance operation and can be listed, streamed, or cancelled.

Restore is two-phase. A user selects a usable snapshot and target profile to create a preview, which validates ownership, target configuration, selection, and overlap constraints. A restore coordinator claims and heartbeats the preview while deriving its work. Consuming a valid preview creates the executable restore job; workers then claim restore items, reconstruct files from pack recipes (using ranges when the provider supports them), apply the chosen conflict policy, and expose run status and a report.

Both coordinators rely on durable status transitions and leases. An expired or failed heartbeat makes recovery possible without treating a process crash as successful completion. Restore credentials are job/run snapshots and OAuth refresh applies to the active snapshot rather than rewriting historical profile data.
