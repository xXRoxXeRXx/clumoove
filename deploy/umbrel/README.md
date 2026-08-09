# Clumoove - Umbrel App Store Packaging Guide

This directory contains the ready-to-use package manifests for publishing **Clumoove** to the official [Umbrel App Store](https://github.com/getumbrel/umbrel-apps).

## Package Files

- `umbrel-app.yml`: Manifest file with store metadata, release info, and port mapping (8380).
- `docker-compose.yml`: Compose file configured for umbrelOS, featuring `app_proxy`, PostgreSQL, Redis, API gateway, worker, and Vite/Nginx frontend.

---

## Submission Steps

1. **Fork the Umbrel App Store Repository**:
   Fork [https://github.com/getumbrel/umbrel-apps](https://github.com/getumbrel/umbrel-apps).

2. **Create a Top-Level `clumoove/` Directory**:
   In your fork, copy the package files into a new top-level folder:
   ```bash
   git checkout -b add-clumoove
   mkdir clumoove
   cp deploy/umbrel/umbrel-app.yml clumoove/
   cp deploy/umbrel/docker-compose.yml clumoove/
   ```

3. **Verify Static Rules & Linter**:
   Run static linter checks in your `umbrel-apps` repository checkout:
   ```bash
   npm run lint:apps -- clumoove --check-images
   git diff --check
   ```

4. **Test on Umbrel / umbrelOS**:
   Deploy and verify on an Umbrel device or containerized umbrelOS test instance:
   ```bash
   umbreld client apps.install.mutate --appId clumoove
   ```

5. **Open Pull Request**:
   Create a Pull Request against `getumbrel/umbrel-apps:master`. Update the `submission` field in `umbrel-app.yml` with your PR URL.
