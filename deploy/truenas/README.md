# Clumoove - TrueNAS SCALE Packaging & Deployment Guide

This directory contains deployment files and official catalog manifests for running **Clumoove** on **TrueNAS SCALE** (Electric Eel 24.10 and newer).

---

## Directory Overview

- `docker-compose.yml`: Ready-to-use Docker Compose configuration for TrueNAS SCALE's **Install via YAML / Custom App** feature.
- `community-app/`: Pre-packaged manifest folder ready to be submitted to the official [truenas/apps](https://github.com/truenas/apps) catalog (`ix-dev/community/clumoove`).
  - `app.yaml`: Metadata for catalog display and versioning.
  - `ix_values.yaml`: Static container image definitions and constants.
  - `questions.yaml`: UI configuration schema for the TrueNAS app install wizard.
  - `templates/docker-compose.yaml`: Jinja2 template processed by TrueNAS library.
  - `templates/test_values/basic-values.yaml`: CI test specification.
- `icon.png`, `icon.svg`, `screenshot-*.png`: Assets for catalog presentation.

---

## Option 1: Direct Installation via TrueNAS SCALE UI (Custom App)

TrueNAS SCALE 24.10+ provides native Docker Compose support.

### 1. Dataset Preparation

Create datasets on your TrueNAS pool (for example under `/mnt/tank/apps/clumoove`):

- `/mnt/tank/apps/clumoove/postgres` (Database)
- `/mnt/tank/apps/clumoove/redis` (Queue)
- `/mnt/tank/apps/clumoove/storage` (Local migration sandbox)

### 2. Deployment in TrueNAS Web UI

1. Open the TrueNAS SCALE Web UI and navigate to **Apps**.
2. Click the three dots (top right) and select **Install via YAML** (or **Custom App**).
3. Paste the contents of [docker-compose.yml](file:///c:/Users/meyer/Development/clumoove/deploy/truenas/docker-compose.yml).
4. Configure your environment variables and dataset paths:
   - `ENCRYPTION_SECRET_KEY`: A secure 32+ character random string.
   - `JWT_SECRET_KEY`: A secure 32+ character random string.
   - `POSTGRES_PASSWORD`: Strong database password.
   - `POSTGRES_DATA_PATH`: `/mnt/tank/apps/clumoove/postgres`
   - `REDIS_DATA_PATH`: `/mnt/tank/apps/clumoove/redis`
   - `STORAGE_DATA_PATH`: `/mnt/tank/apps/clumoove/storage`
5. Click **Save / Install**.
6. Access the Clumoove Web UI at `http://<truenas-ip>:8380`.

---

## Option 2: Submitting to the Official TrueNAS Apps Catalog

To have Clumoove listed in the official TrueNAS App Center:

### 1. Fork and Clone `truenas/apps`

```bash
git clone https://github.com/<your-username>/apps.git
cd apps
git checkout -b add-clumoove
```

### 2. Copy the Community App Files

Copy the contents of `deploy/truenas/community-app/` into `ix-dev/community/clumoove/`:

```bash
mkdir -p ix-dev/community/clumoove
cp -r /path/to/clumoove/deploy/truenas/community-app/* ix-dev/community/clumoove/
```

### 3. Run CI Validation

In the `apps` repository, execute the TrueNAS CI validation script:

```bash
# Using python directly:
python3 .github/scripts/ci.py --train community --app clumoove

# Or using devbox:
devbox shell
python3 .github/scripts/ci.py --train community --app clumoove
```

### 4. Create Pull Request

1. Commit and push the changes to your fork.
2. Open a Pull Request against `truenas/apps:master`.
3. In the PR description, attach `icon.png` and screenshots so TrueNAS maintainers can publish them to `media.sys.truenas.net`.
4. Once merged, Clumoove will automatically appear in TrueNAS SCALE's **Discover Apps** list.
