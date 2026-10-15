---
type: architecture
title: System architecture
description: Clumoove is a containerized, multi-user cloud data platform composed of a React SPA, Go API and worker services, PostgreSQL, Redis, and provider integrations.
tags: [architecture, api, worker, frontend, postgres, redis]
verified:
  - by: openwiki/0.7.2
    at: 2026-10-15T10:57:39.355Z
sources:
  - id: openwiki-source-5d1b0868d855fd62d536acea
    resource: repo://backend/cmd/api/main.go
  - id: openwiki-source-c13e7d9c641d1de1165a6378
    resource: repo://backend/cmd/worker/main.go
  - id: openwiki-source-b79fbbd921df689b4bbdc82f
    resource: repo://docker-compose.yml
  - id: openwiki-source-23775c3de52f3ab95a13cb8b
    resource: repo://README.md
generated: { by: "codex", at: "2026-10-15T10:57:39.355Z" }
---

# System architecture

Clumoove is a cloud-to-cloud migration, synchronization, and backup application. A React 19 single-page application communicates with the Go API over REST and authenticated server-sent events. The API owns authentication, CRUD, job setup, lightweight indexing coordination, administration, the file manager, and scheduler triggers. A separate Go worker owns durable transfer-task processing and the backup and restore coordinators.

PostgreSQL stores application and job state. The worker atomically claims persisted tasks from that database, so deployment can scale with multiple workers. Redis complements it with rate limits, distributed locks, heartbeat/recovery state, pub/sub controls, and short-lived download tickets; it is not the durable task queue.

## Main flows

Migration and sync requests select saved connection profiles and source paths. The API validates ownership and provider configuration, queues indexing or transfer work, and exposes progress. Workers create storage-provider clients, stream data between source and target, persist progress and errors, and verify results according to provider capabilities.

Backup and restore are independent durable workflows. Backup coordinators create and maintain repository snapshots; restore begins with a preview and then consumes an approved restore job. Both use database claims and heartbeats so another worker can recover work after failure.

## Deployment boundary

Compose configurations run the frontend, API, migration worker, PostgreSQL, and Redis as separate services. The frontend is configured with a runtime API URL. API and worker processes each require the encryption key, database, and Redis configuration; only the API requires the distinct JWT signing key. Storage providers are external network dependencies and are accessed only through the provider contract and egress checks.

## Navigation

Read [Persistence, scheduling, and queueing](../architecture/persistence-and-queue.md) for coordination details, [Migrations and synchronization](../workflows/migrations-and-sync.md) and [Backups and restore](../workflows/backups-and-restore.md) for workflow mechanics, and [Security and identity](security-and-identity.md) for trust boundaries.
