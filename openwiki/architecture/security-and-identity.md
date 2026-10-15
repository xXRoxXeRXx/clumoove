---
type: security
title: Security and identity
description: Clumoove combines tenant-scoped authorization, distinct cryptographic keys, encrypted credentials, and guarded network egress.
tags: [security, authentication, encryption, multi-tenancy]
verified:
  - by: openwiki/0.7.2
    at: 2026-10-15T10:57:39.355Z
sources:
  - id: openwiki-source-5d1b0868d855fd62d536acea
    resource: repo://backend/cmd/api/main.go
  - id: openwiki-source-15de01be48b3c20dabba5a7f
    resource: repo://backend/cmd/api/response.go
  - id: openwiki-source-f0ce7b878dca0fb4472eec25
    resource: repo://backend/internal/auth/auth.go
  - id: openwiki-source-30c27e19974be06fef9ceeff
    resource: repo://backend/internal/auth/middleware.go
  - id: openwiki-source-4c45400ba30b1056bd649349
    resource: repo://backend/internal/crypto/crypto.go
  - id: openwiki-source-cf580306bc8dc29e85fe39d0
    resource: repo://backend/internal/storage/ssrf.go
generated: { by: "codex", at: "2026-10-15T10:57:39.355Z" }
---

# Security and identity

The API authenticates requests with short-lived HS256 JWT access tokens. Middleware validates the token and refreshes its authorization state before granting a protected action; temporary 2FA and forced-password-change tokens are deliberately rejected at full-auth boundaries. Refresh tokens are random values stored in `HttpOnly`, `Secure`, `SameSite=None` cookies scoped to `/api/auth`.

Every handler derives the authenticated user ID from request context and passes it into data-access operations, which makes ownership checks part of normal resource access. Administrator routes require both an authenticated token and the administrator role. User suspension, changed roles, and other current authentication state are rechecked rather than relying solely on claims made at token issuance.

## Secrets and encrypted credentials

`ENCRYPTION_SECRET_KEY` and `JWT_SECRET_KEY` have different purposes and must differ at startup. The encryption key is used for AES-256-GCM envelopes holding provider credentials and service secrets; the JWT key signs access-state tokens. Credential encryption has domain separation for values such as OAuth access/refresh tokens and client secrets, reducing the impact of accidental ciphertext substitution between fields.

OAuth application credentials are administrator-managed, and provider operations load encrypted values only around the request that needs them. The code avoids placing plaintext credentials into background goroutines.

## HTTP and egress protections

Middleware applies CORS origin validation, rate limits, security headers, and trusted-proxy handling. The proxy option is safe only when an upstream proxy removes client-supplied forwarding headers. JSON APIs return stable `APIErrorCode` values instead of raw Go errors; the frontend maps those codes into localized messages.

Provider URLs and outbound HTTP use the storage egress validator. It rejects unsafe targets and validates the resolved address at connection time to close DNS-rebinding gaps; plain HTTP provider URLs are not accepted. This validation is required before any request based on a user-supplied provider endpoint.

## Tests

Authentication tests cover token purpose and issuer validation, cookie flags, and authorization-state refresh. Crypto tests cover AES-GCM envelopes and encryption domains, while SSRF and middleware tests exercise egress and browser-facing controls.
