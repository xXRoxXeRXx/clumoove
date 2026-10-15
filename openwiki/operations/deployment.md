---
type: operations
title: Deployment and operations
description: Clumoove deploys as frontend, API, worker, PostgreSQL, and Redis containers configured through a deployment-local environment file.
tags: [deployment, docker, configuration, operations]
verified:
  - by: openwiki/0.7.2
    at: 2026-10-15T10:57:39.355Z
sources:
  - id: openwiki-source-5f5b95b3d6a215fa02ceb945
    resource: repo://.env.example
  - id: openwiki-source-0c106052b3286e779bed85e7
    resource: repo://docker-compose.dev.yml
  - id: openwiki-source-b79fbbd921df689b4bbdc82f
    resource: repo://docker-compose.yml
generated: { by: "codex", at: "2026-10-15T10:57:39.355Z" }
---

# Deployment and operations

Production Compose runs five services: PostgreSQL, Redis, the API, migration worker, and frontend. PostgreSQL and Redis stay on the internal bridge network; the API and frontend publish their application ports. Persistent named volumes retain database and Redis data. The local-storage provider mounts the configured host path into both API and worker containers at `/clumoove`.

Create `.env` from `.env.example` and set strong, different `ENCRYPTION_SECRET_KEY` and `JWT_SECRET_KEY` values plus a unique `REDIS_PASSWORD`. API and worker share the encryption key; the API alone signs JWTs. Do not commit the populated file. OAuth client credentials are configured through the administrator UI, not as Compose environment variables.

`CLUMOOVE_API_URL` configures the production frontend at container startup and must be an origin. `CORS_ALLOWED_ORIGIN`, `FRONTEND_URL`, and `TRUSTED_PROXY` must match the public deployment topology. Enable trusted-proxy handling only behind a proxy that removes client-supplied forwarding headers. `MIGRATION_BLOCK_PRIVATE` can prohibit access to RFC1918 provider endpoints, which is unsuitable for installations intentionally migrating internal servers.

Scale `migration-worker` horizontally for more transfer concurrency. Workers coordinate durable claims through PostgreSQL, but database connection limits must account for worker count and `MAX_THREADS`. `MAX_BACKUP_PACK_WRITERS` and `MAX_RESTORE_PACK_READERS` separately bound backup-repository I/O. The development Compose file uses Air reloaders for the Go services and Vite HMR; production uses the production image targets.
