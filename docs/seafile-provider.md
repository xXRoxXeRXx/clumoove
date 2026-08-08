# Seafile Storage Provider Setup Guide

## Overview

Clumoove supports [Seafile](https://www.seafile.com) as a storage provider via Seafile Web API v2.1. This enables seamless, zero-disk retention data migration and synchronization to and from Seafile libraries.

---

## Configuration & Credentials

When configuring a Seafile connection in Clumoove, provide the following connection details:

| Field | Description | Example |
|---|---|---|
| **Server Address** | Base URL of your Seafile server (HTTP or HTTPS) | `https://seafile.example.com` |
| **Username** | Your Seafile account email or username | `user@example.com` |
| **Password / API Token** | Your Seafile password or Personal Access Token | `••••••••••••` |

---

## Authentication Modes

1. **Username & Password Authentication**:
   - Supply your Seafile account username/email and password.
   - Clumoove authenticates via `/api2/auth-token/` and securely caches the session token.

2. **Personal Access Token**:
   - Leave the username blank and paste your Personal Access Token into the password field.
   - Clumoove will use the token directly for requests (`Authorization: Token <token>`).

---

## Features & Capabilities

- **Zero-Disk Streaming**: Files are transferred via RAM buffers without temporary disk storage.
- **Data Integrity Verification**: Post-transfer verification uses cryptographic SHA-1 hashes calculated by Seafile objects.
- **SSRF Protection**: Outbound requests are re-validated before TCP connections to prevent internal network scanning.
- **Atomic File Overwrites**: Supports atomic temporary upload and rename patterns for safe overwrites.
- **Resource Types**: Supports file and directory transfers within all Seafile libraries.

---

## Security & SSL Certificates

- Seafile connections require valid SSL certificates over HTTPS.
- Plaintext HTTP endpoints are subject to SSRF and egress policies.
