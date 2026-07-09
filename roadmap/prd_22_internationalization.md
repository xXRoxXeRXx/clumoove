# PRD 22: Mehrsprachigkeit (Internationalization / i18n)

## 1. Einleitung & Ziel
Die Plattform soll international einsetzbar sein. Ziel dieses Features ist es, eine vollständige Mehrsprachigkeit (Internationalization, kurz *i18n*) für das gesamte Frontend sowie lokalisierte API-Fehlermeldungen im Backend zu implementieren. Standardmäßig werden die Sprachen **Deutsch (DE)** und **Englisch (EN)** unterstützt, die Architektur muss jedoch für weitere Sprachen (z. B. Französisch, Spanisch) offen sein.

---

## 2. Anwendungsfälle (Use Cases)
*   **UC-1 (Sprachwechsel im UI):** Ein englischsprachiger Administrator ruft das Dashboard auf. Er klickt im Header auf den Sprachwähler und stellt die Sprache von Deutsch auf Englisch um. Alle Bezeichnungen, Formularfelder, Tooltips und Statusmeldungen ändern sich sofort ohne Neuladen der Seite.
*   **UC-2 (Lokalisierte Backend-Fehler):** Die API meldet einen Fehler. Das Backend liefert einen maschinenlesbaren Fehlercode zurück (z. B. `CREDENTIALS_INVALID`). Das Frontend übersetzt diesen Code in die vom Benutzer aktuell gewählte Sprache, anstatt rohe, fest verdrahtete deutsche Fehlertexte anzuzeigen.

---

## 3. Funktionale Anforderungen

| ID | Anforderung | Priorität | Beschreibung |
| :--- | :--- | :--- | :--- |
| **F-01** | Clientseitiges i18n-Framework | MUST | Integration einer stabilen i18n-Bibliothek (z. B. `react-i18next` / `i18next`) im React-Frontend. |
| **F-02** | Sprachwähler | MUST | Eine einfach zugängliche Sprachauswahl (z. B. Flaggen-Symbole oder Dropdown) im Header des Portals. |
| **F-03** | Auto-Erkennung | MUST | Automatische Erkennung der bevorzugten Benutzersprache anhand der Browser-Einstellungen (`navigator.language` / `Accept-Language` Header). |
| **F-04** | Datums- und Zahlenformatierung | MUST | Formatierung von Dateigrößen (z. B. `1,2 MB` in DE vs. `1.2 MB` in EN) und Zeitstempeln nach Ländervorgabe. |
| **F-05** | Standardisierte Error-Codes | MUST | Das Go-Backend liefert strukturierte JSON-Fehlercodes zurück. Lokalisierung der Fehlermeldungen findet im Frontend statt. |

---

## 4. Technische Schnittstellen & Architektur

### Frontend-Struktur (`frontend/src/i18n.ts`)
Konfiguration des Übersetzungs-Dienstes:

```typescript
import i18n from 'i18next';
import { initReactI18next } from 'react-i18next';
import LanguageDetector from 'i18next-browser-languagedetector';

import translationDE from './locales/de/translation.json';
import translationEN from './locales/en/translation.json';

i18n
  .use(LanguageDetector)
  .use(initReactI18next)
  .init({
    resources: {
      de: { translation: translationDE },
      en: { translation: translationEN }
    },
    fallbackLng: 'en',
    interpolation: { escapeValue: false }
  });

export default i18n;
```

### Beispiel Übersetzungsdatei (JSON)
*   **de/translation.json:**
    ```json
    {
      "connect": {
        "title": "Verbindungen einrichten",
        "submit": "Verbindung prüfen"
      },
      "errors": {
        "CREDENTIALS_INVALID": "Die Zugangsdaten sind ungültig. Bitte überprüfe dein Passwort."
      }
    }
    ```
*   **en/translation.json:**
    ```json
    {
      "connect": {
        "title": "Setup Connections",
        "submit": "Verify Connection"
      },
      "errors": {
        "CREDENTIALS_INVALID": "The credentials provided are invalid. Please check your password."
      }
    }
    ```

---

## 5. Sicherheits- und Datenschutzaspekte (DSGVO)
*   Die Sprachpräferenz des Benutzers wird lokal im Browser (LocalStorage oder Cookie) gespeichert. Es werden keine personenbezogenen Daten an externe Übersetzungs-APIs (z. B. Google Translate) übermittelt.

---

## 6. Akzeptanzkriterien
1. Beim ersten Laden des Portals wird die Sprache automatisch basierend auf den Browsersettings des Nutzers geladen.
2. Der Sprachwechsel im Header ändert das gesamte UI augenblicklich (ohne Page-Reload).
3. Backend-Fehler werden übersetzt im UI gerendert.
4. Alle Größenangaben und Datumsangaben passen sich der Länderspezifikation an (z. B. `09.07.2026` in DE vs. `07/09/2026` in EN).
