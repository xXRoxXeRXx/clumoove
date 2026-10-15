---
type: guide
title: Quickstart
description: Start Clumoove locally, understand its five-service topology, and use the OpenWiki pages to navigate architecture and operational concerns.
tags: [quickstart, development, docker, architecture]
verified:
  - by: openwiki/0.7.2
    at: 2026-10-15T10:57:39.355Z
sources:
  - id: openwiki-source-5f5b95b3d6a215fa02ceb945
    resource: repo://.env.example
  - id: openwiki-source-0c106052b3286e779bed85e7
    resource: repo://docker-compose.dev.yml
  - id: openwiki-source-23775c3de52f3ab95a13cb8b
    resource: repo://README.md
generated: { by: "codex", at: "2026-10-15T10:57:39.355Z" }
---

# Quickstart

Clumoove is a multi-user cloud migration platform with migration, recurring sync, and snapshot backup workflows. Its local stack contains a React frontend, Go API, Go worker, PostgreSQL, and Redis. The API serves the browser-facing REST/SSE boundary; the worker executes persisted transfer, backup, and restore work.

## Start the development stack

1. Copy `.env.example` to `.env`.
2. Set strong, different `ENCRYPTION_SECRET_KEY` and `JWT_SECRET_KEY` values and replace the Redis password.
3. Run `docker compose -f docker-compose.dev.yml up --build -d`.
4. Open the frontend on port 3001 and the API on port 8001.

Development Compose uses Vite HMR and Air reload for Go. For a production-style local build, use `docker compose up --build -d`. Before exposing the service, configure the public frontend/API origins and deploy it behind HTTPS. OAuth app credentials are set by an administrator in the application rather than in `.env`.

## Where to go next

- [System architecture](architecture/system-overview.md) explains service boundaries and major flows.
- [Migrations and synchronization](workflows/migrations-and-sync.md) explains indexing, task execution, and sync passes.
- [Backups and restore](workflows/backups-and-restore.md) describes snapshots, verification, previews, and reconstruction.
- [Storage-provider integration](integrations/storage-providers.md) covers the provider contract and verification capabilities.
- [Security and identity](architecture/security-and-identity.md) explains key handling, authentication, ownership, and egress controls.
- [Development and testing](operations/development.md) and [Deployment and operations](operations/deployment.md) cover contributor workflows and running the service.
