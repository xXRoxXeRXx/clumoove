---
type: operations
title: Development and testing
description: Clumoove development spans a Go backend and React frontend with focused unit tests and explicit quality commands.
tags: [development, testing, go, react]
verified:
  - by: openwiki/0.7.2
    at: 2026-10-15T10:57:39.355Z
sources:
  - id: openwiki-source-8037e2358a2c4f9b2c722a11
    resource: repo://AGENTS.md
  - id: openwiki-source-1047363cf615000e4c9bb694
    resource: repo://frontend/package.json
  - id: openwiki-source-f6ccde2440cc497427ba6702
    resource: repo://frontend/vitest.config.ts
  - id: openwiki-source-23775c3de52f3ab95a13cb8b
    resource: repo://README.md
generated: { by: "codex", at: "2026-10-15T10:57:39.355Z" }
---

# Development and testing

Run the complete local stack with Docker Compose, or run the Go API and worker separately when PostgreSQL and Redis are available. The frontend uses Vite for local development. Copy the environment template first and provide distinct encryption and JWT secrets plus Redis configuration.

The standard backend check is `go build ./... && go vet ./...` from `backend`. The frontend type check is `npx tsc --noEmit --project tsconfig.app.json`; lint and test are `npx eslint src && npm test` from `frontend`. `npm test` first checks locale parity and then runs Vitest. The frontend test configuration uses jsdom and discovers `*.test`/`*.spec` TypeScript and TSX files beneath `src`.

Backend tests sit next to the package they verify, including API handlers, authentication, database SQL helpers, queues, schedulers, processors, backup/restore code, and storage providers. Keep tests focused on behavior and invariants. Frontend tests cover components, contexts, utilities, and API client behavior.

Repository invariants are especially important in changes: parameterize all SQL and update both schema definitions, retain encryption/JWT key separation, validate provider URLs before egress, send typed error codes, and maintain German/English locale parity. Use Conventional Commit messages when committing work.
