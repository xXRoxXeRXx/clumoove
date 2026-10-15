---
type: architecture
title: Persistence, scheduling, and queueing
description: Clumoove uses PostgreSQL as the durable system of record and transfer queue, while Redis supplies short-lived coordination and locks.
tags: [postgresql, queue, scheduler, redis]
verified:
  - by: openwiki/0.7.2
    at: 2026-10-15T10:57:39.355Z
sources:
  - id: openwiki-source-acf2436c7dc65235d57835e1
    resource: repo://backend/internal/queue/queue.go
  - id: openwiki-source-3a6375a12a412edc25ff0442
    resource: repo://backend/internal/scheduler/scheduler.go
  - id: openwiki-source-a3f3767b7c2e3655c2fa63cc
    resource: repo://db/schema.sql
  - id: openwiki-source-23775c3de52f3ab95a13cb8b
    resource: repo://README.md
generated: { by: "codex", at: "2026-10-15T10:57:39.355Z" }
---

# Persistence, scheduling, and queueing

PostgreSQL is the durable boundary for Clumoove. It stores tenant-scoped accounts, encrypted connection profiles, jobs, schedules, task progress, backup metadata, restore work, audit data, and reports. `db.InitDB` applies the embedded schema under a PostgreSQL advisory lock, allowing several API or worker processes to start without racing schema initialization.

## Transfer work

Migration work is represented by rows in the task tables rather than a Redis message queue. A worker claims pending work in a transaction with `FOR UPDATE ... SKIP LOCKED`, marks the claim with its worker identity, and processes the item. This lets multiple worker processes distribute durable work without blocking each other. Recovery paths requeue abandoned claims after liveness checks; a restart therefore does not discard unfinished transfers.

Backup verification, restore processing, and maintenance use the same database-oriented claiming pattern. The owning coordinator renews execution heartbeats while it works, and another worker can take over only after the relevant lease expires.

## Redis coordination

Redis is deliberately not a task broker. `queue.Queue` initializes the Redis client and uses it for bounded, expiring coordination: worker-recovery and schedule locks, orphan-recovery locks, OAuth state coordination, cancellation and bandwidth signaling, rate limits, file-manager download tickets, and stream leases. Lock release checks the lock token so one process cannot delete a lock that another process has acquired after expiry.

## Scheduling

API-side schedulers periodically query due schedule rows and use Redis locks to ensure that only one instance evaluates a schedule at a time. They check whether the target job is already active before triggering a migration, sync pass, or backup run. Migrations and backups use cron expressions (backup schedules include an IANA timezone); sync jobs use their persisted interval instead of cron. The scheduler also runs orphaned sync and migration-indexing recovery and repairs scheduled migrations after failures.

## Tests and change points

Queue and scheduler tests cover lock fallbacks, cron validation and next-run calculations, while database tests exercise SQL-level serialization and recovery behavior. Any schema change must be kept in both `db/schema.sql` and the `InitDB` schema definition, so new deployments and already-running installations receive the same structure.
