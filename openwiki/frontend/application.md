---
type: frontend
title: Frontend application
description: The React single-page application drives authenticated migration, sync, backup, profile, file-management, and administrative flows against the Go API.
tags: [react, vite, frontend, i18n]
verified:
  - by: openwiki/0.7.2
    at: 2026-10-15T10:57:39.355Z
sources:
  - id: openwiki-source-454c9bcdde0b77b35e0fc994
    resource: repo://frontend/src/App.tsx
  - id: openwiki-source-8b1d95692265780f30993c49
    resource: repo://frontend/src/i18n.ts
  - id: openwiki-source-4622f09188c7b2fdc738352b
    resource: repo://frontend/src/main.tsx
  - id: openwiki-source-3e5d00bdb1560c2ba7f8c00e
    resource: repo://frontend/src/utils/apiClient.ts
generated: { by: "codex", at: "2026-10-15T10:57:39.355Z" }
---

# Frontend application

The frontend is a React 19/Vite single-page application. `main.tsx` initializes localization, wraps the application in React `StrictMode`, and installs an error boundary; development-only listeners log uncaught browser errors locally. `App.tsx` owns session state, navigation state, deep-link restoration, and composition of the dashboard, migration, sync, backup, profile/file-manager, and administration views. The file manager is loaded lazily.

The API client is configured once with the runtime API URL and current access-token callbacks. `apiFetch` only intercepts requests for that configured API origin. On a non-auth 401 it performs one cookie-based refresh shared by concurrent callers, retries once with the new bearer token, and logs out only when the response still belongs to the current session. JSON helpers normalize API error codes and keep raw backend response messages out of UI text.

Localization uses `i18next` and `react-i18next`, loading German and English translation catalogs with English fallback. New client-visible API errors therefore need keys in both locale files. The application stores the selected language in local storage and resolves the language-only portion of browser preferences.

Component and utility tests use Vitest with jsdom. API-client tests cover refresh and response normalization; component tests cover the major interactive surfaces and shared context providers.
