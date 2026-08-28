# Clumoove - CasaOS & ZimaOS App Store Packaging Guide

This directory contains the ready-to-use package manifests for publishing **Clumoove** to the official [CasaOS AppStore](https://github.com/IceWhaleTech/CasaOS-AppStore) (compatible with CasaOS and ZimaOS).

## Package Files

- `docker-compose.yml`: Full stack definition (Frontend, API Gateway, Migration Worker, PostgreSQL 15, Redis 7) including top-level `x-casaos` v2 AppStore metadata and explicit `type: bind` volume mounts.
- `icon.svg`: App Store icon in vector format.

---

## Volume Mounts in CasaOS

CasaOS stores all persistent container application data under `/DATA/AppData/<app-id>/`:
- `/DATA/AppData/clumoove/storage`: Internal data root for Clumoove local storage.
- `/DATA/AppData/clumoove/postgres`: PostgreSQL database data directory.
- `/DATA/AppData/clumoove/redis`: Redis persistent state.

> **Note for local testing outside CasaOS**:
> If you test this `docker-compose.yml` on a generic Linux/macOS/Windows host where `/DATA` is not automatically managed, ensure the host directories exist beforehand:
> ```bash
> sudo mkdir -p /DATA/AppData/clumoove/{storage,postgres,redis}
> ```

---

## Submission Steps to Official CasaOS AppStore

1. **Fork the CasaOS AppStore Repository**:
   Fork [https://github.com/IceWhaleTech/CasaOS-AppStore](https://github.com/IceWhaleTech/CasaOS-AppStore) to your GitHub account.

2. **Create a Working Branch and App Directory**:
   In your cloned fork:
   ```bash
   git checkout -b add-clumoove
   mkdir -p Apps/Clumoove
   ```

3. **Copy Package Files**:
   Copy the manifests and assets into `Apps/Clumoove/`:
   ```bash
   cp deploy/casaos/docker-compose.yml Apps/Clumoove/
   cp deploy/casaos/icon.svg Apps/Clumoove/
   ```
   *(Optional)*: Add `thumbnail.png` and `screenshot-1.png` to `Apps/Clumoove/` if you want rich gallery previews in the store.

4. **Validate Docker Compose Configuration**:
   Ensure the YAML syntax and schema are valid:
   ```bash
   docker compose -f Apps/Clumoove/docker-compose.yml config -q
   ```

5. **Local Testing in CasaOS**:
   - Open your CasaOS Dashboard.
   - Click **App Store** -> **Custom Install** (top right).
   - Click **Import** (top right of modal) and paste the contents of `deploy/casaos/docker-compose.yml`.
   - Verify that all services start up, the WebUI opens on port `8380`, and health checks pass.

6. **Open Pull Request**:
   - Push your branch `add-clumoove` to your fork.
   - Open a Pull Request against `IceWhaleTech/CasaOS-AppStore:main`.
   - Title: `feat: add Clumoove app`
   - Fill out the PR template confirming local testing and validation.
