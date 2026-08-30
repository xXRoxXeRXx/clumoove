# 03 – Frontend

The frontend is a **React 19 + TypeScript** single-page application, bundled with **Vite 8** and
styled with **Tailwind CSS v4** (via `@tailwindcss/vite`). Icons come exclusively from
`@heroicons/react`. All user
strings are localized with `i18next` + `react-i18next` + `i18next-browser-languagedetector`.

The connection wizard has an Immich branch with server URL and API-key fields (no username), a least-privilege permission hint, `/Timeline` default selection, album browsing/creation, native-duplicate `SKIP`, and migration-only mode. Sync, backup, and restore are unavailable whenever either endpoint is Immich.

## Cloud File Manager (Phase 1)

`FileManager` uses saved profile IDs, opaque server references, paginated listings, breadcrumbs, ticketed downloads, and a capability-gated upload queue. `useAppHistory` persists only `?view=files&profile=<uuid>`; remote paths, names, item references, and tickets remain out of the URL. The queue sends immutable `File` bodies through XHR, runs at most four uploads, and retains failed or cancelled items for manual retry. The modal preview fetches a one-time download ticket into a local Blob URL, then renders safe images/media/text/PDF/DOCX/XLSX content. PDF uses bundled `react-pdf`/`pdfjs-dist`; DOCX (`mammoth`) and XLSX (`xlsx`) parsers run in terminating workers, and DOCX HTML is restricted with `dompurify`. MIME agreement and fixed per-format limits determine whether a file is previewed or download-only.

---

## 1. Tech Stack

| Concern | Choice |
| :------ | :----- |
| Framework | React 19 (`react`, `react-dom`) |
| Language | TypeScript 6 (`typescript` ^5.9) |
| Bundler / Dev server | Vite 8 (`vite` ^8) |
| Styling | Tailwind CSS v4 (`tailwindcss` ^4, `@tailwindcss/vite`) |
| Icons | `@heroicons/react` ^2 |
| i18n | `i18next` ^26, `react-i18next` ^17, `i18next-browser-languagedetector` ^8 |
| Lint | ESLint 10 + `typescript-eslint` + react-hooks/refresh plugins |

Scripts (`package.json`): `dev` (`vite`), `build` (`tsc -b && vite build`), `lint` (`eslint .`),
`locale:check` (`node scripts/check-locales.mjs`), `test` (`npm run locale:check && vitest run`), and
`preview` (`vite preview`). `locale:check` is mandatory after i18n or API-error-code changes: it verifies
locale-key parity and error-code coverage before tests run.

---

## 2. Project Structure

```
frontend/src/
├── main.tsx                 # React root, mounts <App/>, imports i18n
├── App.tsx                  # Top-level state machine, history routing, auth bootstrap
├── index.css                # Global Tailwind layers and CSS variables (theming)
├── i18n.ts                  # i18next init (de/en, fallback en)
├── types.ts                 # Shared TypeScript types (User, MigrationConfig, BackupJob, RestorePreview, …)
├── assets/                  # Static images (hero, logos, svgs)
├── components/
│   ├── AuthForm.tsx         # Login / register / 2FA / forgot-password entry
│   ├── ConnectForm.tsx      # Source/target provider selection + connection test
│   ├── FileBrowser.tsx      # Path/calendar/contact selection before start
│   ├── BackupDashboard.tsx  # Backup-job list and global backup SSE stream
│   ├── BackupOptionsForm.tsx # Backup schedule (cron/timezone), retention count, compression
│   ├── BackupSnapshotBrowser.tsx # Snapshot tree, verification, and restore-preview workflow
│   ├── Dashboard.tsx        # Live migration progress (SSE)
│   ├── EditBackupModal.tsx  # Edit backup schedule and retention settings
│   ├── EditSyncModal.tsx    # Browse and edit a sync job's scope and schedule
│   ├── FileManager/         # Profile-bound file manager and preview/upload controls
│   ├── MigrationsDashboard.tsx # History list of the user's migrations, syncs, and backups
│   ├── SyncDashboard.tsx    # Live sync progress and delta stats
│   ├── SettingsPage.tsx     # Profile, password, 2FA, email, SMTP, avatar
│   ├── AdminPanel.tsx       # ADMIN-only user/migration/audit oversight
│   ├── ResetPasswordForm.tsx
│   ├── ConfirmEmailChangeForm.tsx
│   ├── AvatarCropper.tsx
│   ├── LanguageSwitcher.tsx
│   └── Toggle.tsx
├── api/
│   ├── files.ts             # File-manager API requests and binary upload/thumbnail helpers
│   └── profiles.ts          # Connection-profile API helpers
├── contexts/
│   ├── ThemeContext.tsx     # Light/dark theme provider
│   └── useThemeContext.ts
├── hooks/
│   ├── useAppHistory.ts     # URL/history-backed step state
│   └── useTheme.ts
├── locales/
│   ├── de/translation.json  # German strings (incl. errors.* namespace)
│   └── en/translation.json  # English strings (key parity required)
└── utils/
    ├── adminApi.ts          # Admin REST helpers
    ├── apiClient.ts         # Configured apiFetch wrapper with single-flight refresh
    ├── apiError.ts          # useApiError() → translateApiError()
    ├── format.ts            # Locale-aware number/date/bytes formatting (useFormat)
    ├── oauth.ts             # OAuth popup + postMessage receiver
    └── runtimeConfig.ts     # Runtime/Vite API-origin validation
```

---

## 3. Application State & Routing

`App.tsx` is a step-based state machine (no React Router). Steps:

```ts
type AppStep =
  | 'login' | 'history' | 'connect' | 'select' | 'dashboard'
  | 'settings' | 'admin' | 'reset-password' | 'confirm-email'
  | 'syncdetail' | 'backupdetail' | 'files';
```

- Initial step is derived from URL params (`reset-token`, `email-change-token`) or `localStorage`
  session state.
- **History-based navigation:** in-app screens are pushed/replaced via `window.history.pushState` and
  query parameters for migration, sync, backup, profile, or settings state. `goToOverview` / `goBack` use
  `history.back()` so browser back/forward works deterministically and stale credentials are cleared when
  leaving `select`/`dashboard`.
- **Silent login:** on mount, if `has_session` is set, it calls `POST /api/auth/refresh` then
  `GET /api/auth/me`; on success it restores the dashboard/history step.
- **Auto token refresh:** the configured `apiFetch` wrapper adds the bearer token and credentials to calls
  for the configured API origin. On a non-auth `401`, it performs one single-flight cookie refresh and
  retries the request with the new bearer token. Direct `fetch` calls remain for public settings, refresh
  and logout, and special binary/ticket flows. A separate `setInterval` refreshes the access token every
  14 minutes.
- **OAuth:** the OAuth callback window posts tokens via `postMessage`; the receiver validates
  `event.origin` against the API origin before trusting it (tokens are in-memory only).

---

## 4. API URL Resolution

`App.tsx → getApiUrl()` resolves the backend URL dynamically (see also
[Deployment](./08-deployment.md)):

1. In the production image, `/runtime-config.js` supplies a validated `CLUMOOVE_API_URL` origin at
   container startup. It takes precedence and supports a configured cross-origin API.
2. In Vite development, `import.meta.env.VITE_API_URL` is the build/dev-server fallback.
3. Without either configured origin, `localhost` or `127.0.0.1` on port `3000` uses
   `http://<hostname>:8001` for the standalone development setup.
4. Every other case uses the same origin (production nginx, Umbrel app proxy, custom domain, or Vite
   unless `VITE_API_URL` is set).

`CLUMOOVE_API_URL` must be an origin-only HTTP(S) URL: it cannot contain a path, credentials, query, or
fragment. nginx derives `connect-src` from that exact value, while an unset value leaves the CSP at
`connect-src 'self'` for same-origin API proxying. `parseApiOrigin` rejects plaintext `http:` origins in
production as well as origins with credentials, paths, queries, or fragments. An origin-only `VITE_API_URL`
takes precedence even when it points to `localhost` or `127.0.0.1`, making an explicit Vite development API
target deterministic.

---

## 5. Internationalization (i18n)

- `i18n.ts` initializes with resources `de`/`en`, `fallbackLng: 'en'`, `supportedLngs: ['de','en']`,
  `load: 'languageOnly'`, detector order `localStorage → navigator → htmlTag`. Once authenticated, the
  persisted account language overrides the detected value so background emails and notifications use the
  same language.
- Delivery strings live under `delivery.*` in the same locale files. Docker build stages regenerate the
  worker/API catalog from those source files before compiling; use `cd backend && go generate ./internal/i18n`
  only before a direct local Go build.
- Both `locales/de/translation.json` and `locales/en/translation.json` **must stay in key parity** —
  every key present in one must exist in the other.
- **Error codes:** The backend sends **only** a machine-readable `error_code` (never human text).
  `useApiError()` (`utils/apiError.ts`) maps it to a localized string under the `errors.*` namespace
  (`t('errors.' + code)`), falling back to `errors.UNKNOWN`. New backend `APIErrorCode` values must be
  added to both locale files.
- **Formatting:** Locale-aware formatting lives in `utils/format.ts` (`formatBytes`, `formatDate`,
  `formatDateTime`, `useFormat`). Never hand-format with `toFixed`/`toLocaleString` without passing the
  active language.

---

## 6. Key Components

| Component | Role |
| :-------- | :--- |
| `AuthForm` | Login, registration, TOTP code entry, password reset request. |
| `ConnectForm` | Choose source/target provider + credentials; supports saved connection profiles; calls `/migration/connect`; on success hands config + listed files to the next step. |
| `FileBrowser` | Pick paths/calendars/contacts, conflict strategy, target dir, threads, bandwidth, optional `scheduled_time`; calls `/migration/start`; drops secrets from memory after success. |
| `BackupOptionsForm` | Configure backup schedules (cron, timezone), retention limits (1–365), source paths, and triggers repository initialization. |
| `BackupSnapshotBrowser` | Explore deduplicated point-in-time snapshots, download specific items, trigger metadata/budgeted repository verification, and own the restore-preview workflow and its conflict-analysis UI. |
| `Dashboard` | Live progress for a migration via authenticated `/migration/{id}/stream` SSE using `connectSseLoop`; shows files/calendars/contacts stats, pause/resume/cancel, threads/bandwidth controls, a paginated in-app error overview, and CSV report download. |
| `MigrationsDashboard` | Lists the user's migrations, sync jobs, and backup jobs with status; opens a selected job or starts a new one. |
| `SyncDashboard` | Live progress and details for synchronization jobs (delta stats, changed/deleted files, pause/resume, threads and bandwidth controls) plus a paginated in-app error overview. |
| `SettingsPage` | Display name, password change, avatar (cropper), 2FA setup/enable/disable, email change, an email-notification toggle when the instance mailer is available, connection profile management. |
| `AdminPanel` | (ADMIN) user list/suspend/reactivate/delete/role, global stats, all-migrations view, all-syncs view, audit log, and central SMTP configuration/test/removal. |
| `LanguageSwitcher` | Switch `de`/`en`; persisted locally and to the authenticated account. |

---

## 7. Security Notes (Frontend)

- Secrets (source/target passwords, OAuth tokens, SFTP keys) are held **in memory only** and explicitly
  cleared (`setCredentials(null)`) after a migration is created or when navigating away from
  `select`/`dashboard`.
- The refresh token lives in an HTTP-only cookie (set by the backend); the access token is in memory.
- `apiError.ts` never surfaces raw backend error text; only localized translations of `error_code`.
- CSV formula-injection is neutralized server-side; the client renders the report as plain text.
- Plaintext-HTTP API usage triggers a console warning.

---

## 8. UI System, Theming, and Accessibility

`ThemeContext` provides light/dark mode via semantic CSS custom properties in `index.css`. The visual
language is intentionally compact and neutral: zinc surfaces, 1px borders, small radii, no decorative
gradients, glass effects, backdrop blur, large shadows, or scale-based hover effects. Components must not
introduce legacy `portal-*`, `glass-*`, `shadow-portal*`, or direct palette-based action/status surfaces.

Use shared `ui-*` Tailwind utilities instead of reimplementing control styles in views:

- Layout and controls: `ui-card`, `ui-section`, `ui-input`, `ui-select`, `ui-checkbox`, and `ui-radio`.
- Actions: `ui-button-primary`, `ui-button-secondary`, `ui-button-danger`, `ui-button-quiet`, and
  `ui-icon-button`. Primary actions retain visible text; icons are reserved for navigation, context, and
  icon-only actions.
- Feedback and data: `ui-alert` variants, `ui-badge` variants, `ui-progress`, `ui-table`, `ui-empty`,
  `ui-loading`, and `ui-pagination`. `StatusBadge.tsx` owns application-status-to-variant mapping.

Semantic theme tokens cover primary, success, information, warning, and danger in both themes. Raw Tailwind
palette colours are allowed only for constrained file-type icon context, not for surfaces, buttons, focus
rings, or status mappings. `prefers-reduced-motion` disables nonessential transitions and animations.

Interactive requirements:

- Use native `button`, `a`, `input`, `select`, and `textarea` controls whenever possible. Do not make
  unsemantic containers clickable or nest interactive controls inside another interactive control.
- Icon-only buttons require localized `aria-label` and `title`.
- Tabs and menus provide their expected keyboard controls. Dialogs use `role="dialog"`, `aria-modal`, a
  programmatic name, Escape handling, focus trap, and focus restoration. Reuse `useFocusTrap` and the
  `ConfirmationDialog` pattern rather than bespoke modal behavior.
- Tree and path-selection rows expose explicit keyboard-operable expand and selection controls.

The **Administration → System** tab uses a two-column `md:grid-cols-2` layout: the left column holds the
general settings (registration toggle) and the email-server card; the right column holds the read-only OAuth
callback-URL card and one card per OAuth provider (Google, OneDrive, Dropbox, HiDrive). All cards reuse the
shared `SectionCard` component; the email-server card renders its form in a single column because it occupies
half the width.

### 8.1 Restore and Repository-Check UI

The snapshot browser supports a saved target profile or a direct files target. Direct passwords/access
tokens and OAuth refresh tokens exist only in component state until preview submission, then the browser
clears them. Immich is omitted from both pickers. The mandatory preview dialog presents point-in-time
conflict counts, type conflicts, expected skips/renames, unavailable items, metadata warnings, and a bounded
example list before the one-time start action. Restore and repository-check history expose cancellation,
progress, reports, and damage counters; all progress state is safe to refresh after a reconnect.

---

## 9. Frontend Validation

Run the standard typecheck and lint commands from [Development §2](./09-development.md#2-code-quality--checks),
then run `npm run locale:check`, `npm test`, and `npm run build` for a complete frontend change. `npm test`
runs the locale/error-code validation before Vitest; run `npm run locale:check` directly for a fast
parity check after translation or API-error-code edits. UI changes additionally require:

- a search confirming no legacy visual utilities or `lucide-react` imports remain;
- locale key-parity verification after adding visible text or accessible labels; and
- a light/dark, narrow/wide, and keyboard review of affected controls, menus, tabs, dialogs, and trees.
