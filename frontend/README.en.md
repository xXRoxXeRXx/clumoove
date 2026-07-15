# Clumoove Frontend

React 19 SPA (TypeScript 6) for the [Clumoove Multi-Cloud Migration Platform](../../README.md). Bundled with Vite 8, styled with Tailwind CSS v4, internationalized with `i18next` (`de` / `en`).

> 📘 **Deutsch:** [README.md](./README.md)

## Tech Stack
- **React 19** + **TypeScript 6**
- **Vite 8** (dev server & build)
- **Tailwind CSS v4** (`@tailwindcss/vite`)
- **Lucide React** (icons)
- **i18next** + **react-i18next** + `i18next-browser-languagedetector`

## Scripts
```bash
npm install      # install dependencies
npm run dev      # Vite dev server (default: http://localhost:5173)
npm run build    # typecheck (tsc -b) + production build (dist/)
npm run preview  # preview the build locally
npm run lint     # ESLint
```

## Configuration
The API URL is resolved automatically (`src/utils/api.ts`):
- `VITE_API_URL` set and not `localhost` → used directly (production proxy).
- Custom domain without port → reverse-proxy routing.
- Local → port `8001` (the API container is mapped to host port `8001`).

Error codes from the backend are localized via the locale tables (`src/locales/{de,en}/translation.json`, `errors.*` namespace) – the frontend never reads raw `error`/`message` text.

## Directory Structure (Excerpt)
```
frontend/
├── src/
│   ├── components/     # UI components (dashboard, file browser, settings, …)
│   ├── i18n.ts         # i18next initialization
│   ├── locales/        # de / en translations (key parity required)
│   └── utils/          # api.ts, apiError.ts, format.ts
├── public/             # static assets (logo.png, …)
├── index.html
└── vite.config.ts
```

See the main README for architecture, storage providers, the security model and the API overview.
