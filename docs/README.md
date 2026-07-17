# Clumoove – Documentation

Welcome to the technical documentation for **Clumoove**, the multi-cloud migration platform.
This documentation complements the main [README](../README.md) (English)
and describes the system in detail for developers, operators, and architects.

> **Note on language & code:** All explanatory text is written in English. Code identifiers,
> environment variables, and protocol names remain in their English form.

---

## Table of Contents

| # | Document | Contents |
| :- | :------- | :------- |
| 01 | [Architecture](./01-architecture.md) | Components, data flow, migration lifecycle, resilience concepts |
| 02 | [Backend](./02-backend.md) | Go modules, packages (`db`, `queue`, `processor`, `scheduler`, `indexer`, `storage`, `auth`, `crypto`, …), startup logic |
| 03 | [Frontend](./03-frontend.md) | React SPA, components, routing, i18n, API client, theming |
| 04 | [API Reference](./04-api-reference.md) | Full REST/WebSocket endpoint list with protection and semantics |
| 05 | [Storage Providers](./05-storage-providers.md) | `StorageProvider` interface, supported providers, factory, SSRF protection |
| 06 | [Database Schema](./06-database.md) | Tables, columns, indexes, triggers, auto-migration |
| 07 | [Security Model](./07-security.md) | Key segregation, encryption, OAuth, CORS, rate limiting, audit log |
| 08 | [Deployment & Operations](./08-deployment.md) | Docker Compose, env vars, scaling, ops tasks |
| 09 | [Development](./09-development.md) | Local setup without Docker, code quality, conventions |

There is also a conceptual document:
[Service Comparison (Migration/Sync/Backup)](./service_comparison.md).

---

## Quick Overview

- **Backend:** One Go module (`backend/`) with two entrypoints — `cmd/api` (HTTP gateway) and
  `cmd/worker` (migration engine). Routing uses the Go 1.22 standard mux (no third-party router libs).
- **Queue:** Native in PostgreSQL (`SELECT … FOR UPDATE SKIP LOCKED`). Redis is used **only** for
  worker heartbeats, distributed recovery locks (`SET NX`), and cancel/bandwidth Pub/Sub.
- **Frontend:** React 19 + TypeScript SPA, bundled with Vite 8, Tailwind CSS v4.
- **Data:** PostgreSQL 15 (metadata, users, tasks, schedules, audit log) + Redis 7 (coordination).
- **Languages:** `de` (fallback) and `en`, localized via `i18next`/`react-i18next`.

---

## Quickstart (reference)

```bash
cp .env.example .env   # fill ENCRYPTION_SECRET_KEY / JWT_SECRET_KEY (each: openssl rand -base64 32)
docker compose -f docker-compose.dev.yml up --build -d        # development (local build)
# docker compose up -d                                        # production (prebuilt GHCR images)
```

Frontend: http://localhost:3001 · API: http://localhost:8001
