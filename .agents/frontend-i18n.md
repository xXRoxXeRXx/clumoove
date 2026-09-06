# Frontend & Internationalization

## UI Styling & Tokens
- **Design System Tokens**: Use semantic `ui-*` utility classes and color tokens defined in [frontend/src/index.css](file:///c:/Users/meyer/Development/clumoove/frontend/src/index.css).
- **Styling Prohibitions**: Do not reintroduce deprecated patterns such as `portal-*`, `glass-*`, `shadow-portal*`, decorative gradient overlays, heavy backdrop blur, oversize shadows, or scaling hover effects. Keep interfaces clean, responsive, and token-driven.
- **Icons**:
  - Use `@heroicons/react` for all UI controls, navigation, and state indicators.
  - `react-icons` is strictly reserved for external provider brand logos in `components/connect/ProviderIcon.tsx`.
  - Every icon-only action button must specify localized `aria-label` and `title` attributes.
- **Accessibility & Focus**:
  - Use native HTML semantics whenever possible.
  - Modals and dialogs must implement accessible modal semantics: accessible label, Escape dismissal, focus trapping, and focus restoration upon close.
  - Menus, tabs, and trees must be fully keyboard-navigable.

## API Error Handling & Error Codes
- **Machine-Readable Protocol**:
  - The backend returns machine-readable error codes (`APIErrorCode`) in JSON responses — never raw English text or internal error strings.
  - For validation or business conflicts, use `writeValidationError(w, code)` (400) or `writeConflictError(w, code)` (409).
  - Connection probes (`/connect`, `/browse`, `/target/browse`, `mkdir`) return `HTTP 200` with `{"success": false, "error_code": "..."}` for logical failures so the frontend can translate them smoothly.
- **Frontend Consumption**:
  - Components retrieve the translation function via `useApiError()` in `src/utils/apiError.ts`:
    ```typescript
    const { translateApiError } = useApiError();
    // ...
    const body = await res.json().catch(() => ({}));
    const message = translateApiError(body.error_code);
    ```
  - Never inspect raw `error` or `message` strings on responses.

## Internationalization (i18n)
- **Library & Setup**: Managed by `i18next`, `react-i18next`, and `i18next-browser-languagedetector` in `src/i18n.ts`. Supported languages: `de` and `en` (with fallback `en`).
- **Locale Parity**:
  - Locale catalogs reside in `src/locales/{de,en}/translation.json`.
  - **Strict Key Parity**: Any key added to `de` must also exist in `en` and vice-versa.
  - Error codes are localized under `errors.<CODE>`. If a code is not mapped, `translateApiError` defaults to `errors.UNKNOWN`.
- **Formatting Utilities**: Use `src/utils/format.ts` (`formatBytes`, `formatDate`, `formatDateTime`, `useFormat`) for locale-aware formatting. Never invoke `toLocaleString` or `toFixed` without passing the active locale.
- **Delivery Catalog**: Email and notification templates reside under `delivery.*` in the frontend translations. Run `(cd backend && go generate ./internal/i18n)` to regenerate the Go catalog when editing delivery keys.
