# Clumove Frontend

React-19-SPA (TypeScript 6) für die [Multi-Cloud-Migrationsplattform Clumove](../../README.md). Gebündelt mit Vite 8, gestylt mit Tailwind CSS v4, internationalisiert mit `i18next` (`de` / `en`).

> 📘 **English:** [README.en.md](./README.en.md)

## Tech-Stack
- **React 19** + **TypeScript 6**
- **Vite 8** (Dev-Server & Build)
- **Tailwind CSS v4** (`@tailwindcss/vite`)
- **Lucide React** (Icons)
- **i18next** + **react-i18next** + `i18next-browser-languagedetector`

## Skripte
```bash
npm install      # Dependencies installieren
npm run dev      # Vite-Dev-Server (Standard: http://localhost:5173)
npm run build    # Typcheck (tsc -b) + Produktions-Build (dist/)
npm run preview  # Build lokal voranschauen
npm run lint     # ESLint
```

## Konfiguration
Die API-URL wird automatisch aufgelöst (`src/utils/api.ts`):
- `VITE_API_URL` gesetzt und kein `localhost` → direkt (Produktions-Proxy).
- Benutzerdefinierte Domain ohne Port → Reverse-Proxy-Routing.
- Lokal → Port `8001` (API-Container ist auf Host-Port `8001` gemappt).

Fehlercodes aus dem Backend werden über die Locale-Tabellen (`src/locales/{de,en}/translation.json`, Namespace `errors.*`) lokalisiert – das Frontend liest niemals rohe `error`/`message`-Texte.

## Verzeichnisstruktur (Auszug)
```
frontend/
├── src/
│   ├── components/     # UI-Komponenten (Dashboard, FileBrowser, Settings, …)
│   ├── i18n.ts         # i18next-Initialisierung
│   ├── locales/        # de / en Übersetzungen (Key-Parität erforderlich)
│   └── utils/          # api.ts, apiError.ts, format.ts
├── public/             # Statische Assets (logo.png, …)
├── index.html
└── vite.config.ts
```

Siehe das Haupt-README für Architektur, Storage-Anbieter, Sicherheitsmodell und API-Überblick.
