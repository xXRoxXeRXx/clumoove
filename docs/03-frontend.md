# 03 – Frontend

The frontend is a **React 19 + TypeScript** single-page application, bundled with **Vite 8** and
styled with **Tailwind CSS v4** (via `@tailwindcss/vite`). Icons come from `lucide-react`. All user
strings are localized with `i18next` + `react-i18next` + `i18next-browser-languagedetector`.

---

## 1. Tech Stack

| Concern | Choice |
| :------ | :----- |
| Framework | React 19 (`react`, `react-dom`) |
| Language | TypeScript 6 (`typescript` ^5.9) |
| Bundler / Dev server | Vite 8 (`vite` ^8) |
| Styling | Tailwind CSS v4 (`tailwindcss` ^4, `@tailwindcss/vite`) |
| Icons | `lucide-react` |
| i18n | `i18next` ^26, `react-i18next` ^17, `i18next-browser-languagedetector` ^8 |
| Lint | ESLint 10 + `typescript-eslint` + react-hooks/refresh plugins |

Scripts (`package.json`): `dev` (`vite`), `build` (`tsc -b && vite build`), `lint` (`eslint .`),
`preview` (`vite preview`).

---

## 2. Project Structure

```
frontend/src/
├── main.tsx                 # React root, mounts <App/>, imports i18n
├── App.tsx                  # Top-level state machine, history routing, auth bootstrap
├── App.css / index.css      # Global + Tailwind layers, CSS variables (theming)
├── i18n.ts                  # i18next init (de/en, fallback en)
├── types.ts                 # Shared TypeScript types (User, MigrationConfig, CloudFile, …)
├── assets/                  # Static images (hero, logos, svgs)
├── components/
│   ├── AuthForm.tsx         # Login / register / 2FA / forgot-password entry
│   ├── ConnectForm.tsx      # Source/target provider selection + connection test
│   ├── FileBrowser.tsx      # Path/calendar/contact selection before start
│   ├── Dashboard.tsx        # Live migration progress (WebSocket / SSE)
│   ├── MigrationsDashboard.tsx # History list of the user's migrations
│   ├── SettingsPage.tsx     # Profile, password, 2FA, email, SMTP, avatar
│   ├── AdminPanel.tsx       # ADMIN-only user/migration/audit oversight
│   ├── ResetPasswordForm.tsx
│   ├── ConfirmEmailChangeForm.tsx
│   ├── AvatarCropper.tsx
│   ├── LanguageSwitcher.tsx
│   └── Toggle.tsx
├── contexts/
│   ├── ThemeContext.tsx     # Light/dark theme provider
│   └── useThemeContext.ts
├── hooks/
│   └── useTheme.ts
├── locales/
│   ├── de/translation.json  # German strings (incl. errors.* namespace)
│   └── en/translation.json  # English strings (key parity required)
└── utils/
    ├── adminApi.ts          # Admin REST helpers
    ├── apiError.ts          # useApiError() → translateApiError()
    ├── format.ts            # Locale-aware number/date/bytes formatting (useFormat)
    └── oauth.ts             # OAuth popup + postMessage receiver
```

---

## 3. Application State & Routing

`App.tsx` is a step-based state machine (no React Router). Steps:

```ts
type Step =
  | 'login' | 'history' | 'connect' | 'select'
  | 'dashboard' | 'settings' | 'admin'
  | 'reset-password' | 'confirm-email';
```

- Initial step is derived from URL params (`reset-token`, `email-change-token`) or `localStorage`
  session state.
- **History-based navigation:** in-app screens are pushed/replaced via `window.history.pushState`
  with a state object `{ step, migration, appEntry }`. `goToOverview` / `goBack` use `history.back()`
  so browser back/forward works deterministically and stale credentials are cleared when leaving
  `select`/`dashboard`.
- **Silent login:** on mount, if `has_session` is set, it calls `POST /api/auth/refresh` then
  `GET /api/auth/me`; on success it restores the dashboard/history step.
- **Auto token refresh:** a global `fetch` patch intercepts `401` responses (except auth endpoints),
  performs a silent refresh, and replays the original request with the new bearer token. A second
  `setInterval` refreshes the access token every 14 minutes.
- **OAuth:** the OAuth callback window posts tokens via `postMessage`; the receiver validates
  `event.origin` against the API origin before trusting it (tokens are in-memory only).

---

## 4. API URL Resolution

`App.tsx → getApiUrl()` resolves the backend URL dynamically (see also
[Deployment](./08-deployment.md)):

1. If `import.meta.env.VITE_API_URL` is set and **not** localhost/127.0.0.1 → use it (production proxy).
2. Else, if on a custom domain with no/80/443 port → use same host without a port (reverse proxy).
3. Else → `http://<hostname>:8001` (local dev).

A console warning is emitted when the API is reached over plaintext HTTP on a non-loopback host.

---

## 5. Internationalization (i18n)

- `i18n.ts` initializes with resources `de`/`en`, `fallbackLng: 'en'`, `supportedLngs: ['de','en']`,
  `load: 'languageOnly'`, detector order `localStorage → navigator → htmlTag`.
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
| `ConnectForm` | Choose source/target provider + credentials; calls `/migration/connect`; on success hands config + listed files to the next step. |
| `FileBrowser` | Pick paths/calendars/contacts, conflict strategy, target dir, threads, bandwidth, optional `scheduled_time`; calls `/migration/start`; drops secrets from memory after success. |
| `Dashboard` | Live progress for a migration via the `/migration/{id}/ws` WebSocket (token query param); shows files/calendars/contacts stats, pause/resume/cancel, threads/bandwidth controls, CSV report download. |
| `MigrationsDashboard` | Lists the user's migrations with status; opens a selected migration or starts a new one. |
| `SettingsPage` | Display name, password change, avatar (cropper), 2FA setup/enable/disable, email change, per-user SMTP settings + test. |
| `AdminPanel` | (ADMIN) user list/suspend/reactivate/delete/role, global stats, all-migrations view, audit log. |
| `LanguageSwitcher` | Switch `de`/`en`; persisted to `localStorage` by the detector. |

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

## 8. Theming

`ThemeContext` provides light/dark mode via CSS custom properties defined in `index.css` (e.g.
`--color-bg-primary`, `--color-text-primary`, `--color-border`, `--color-glass-*`,
`--color-portal-navy-themed`). Components reference these variables (with Tailwind's arbitrary value
syntax) so a single theme switch restyles the entire glassmorphism UI.
