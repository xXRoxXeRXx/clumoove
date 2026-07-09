# PRD 23: OAuth OpenID Connect Login (SSO / OIDC)

## 1. Einleitung & Ziel
Für den produktiven Einsatz im Unternehmensbereich reicht ein einfaches, lokales Login-Modell oft nicht aus. Ziel dieses Features ist es, die Benutzerauthentifizierung für das Migrations-Portal um **OpenID Connect (OIDC / OAuth2)** zu erweitern. 

Dies ermöglicht Single-Sign-On (SSO) über zentrale Identity Provider (IdPs) wie Keycloak, Authentik, Google Workspace, Okta oder Microsoft Azure AD.

---

## 2. Anwendungsfälle (Use Cases)
*   **UC-1 (Unternehmens-SSO):** Ein IT-Mitarbeiter ruft das Portal auf. Statt ein neues Passwort zu vergeben, klickt er auf "Anmelden mit Firmen-Account". Er wird zum Keycloak-Server der Firma weitergeleitet, authentifiziert sich dort (z. B. via Windows Hello oder Authenticator-App) und wird als autorisierter Benutzer ins Migrations-Portal eingeloggt.
*   **UC-2 (Automatische Benutzerregistrierung):** Meldet sich ein neuer Benutzer zum ersten Mal über OIDC an, wird im Go-Backend automatisch ein passender Benutzerdatensatz in der Tabelle `users` angelegt und seine Berechtigungen entsprechend seiner OIDC-Rollen vergeben.

---

## 3. Funktionale Anforderungen

| ID | Anforderung | Priorität | Beschreibung |
| :--- | :--- | :--- | :--- |
| **F-01** | OIDC-Standard Konformität | MUST | Unterstützung des OIDC Authorization Code Flows mit PKCE (Proof Key for Code Exchange) zur sicheren Client-Authentifizierung. |
| **F-02** | Provider-Konfiguration | MUST | Konfigurierbarkeit von: Client ID, Client Secret, Issuer-URL (Discovery-Endpunkt `.well-known/openid-configuration`) und Redirect-URI. |
| **F-03** | Claims-Parsing | MUST | Auslesen der Standard-Claims im ID-Token (`sub` als eindeutige ID, `email` zur Identifikation, `name` zur Anzeige). |
| **F-04** | Rollen- und Rechtezuweisung | SHOULD | Auslesen von Gruppen- oder Rollen-Claims (z. B. `groups` oder `roles` aus dem ID-Token) zur Zuweisung von Administratorrechten im Portal. |
| **F-05** | Fallback-Login | SHOULD | Optionale Beibehaltung des lokalen Logins für Notfall-Administratoren (Break-Glass Accounts), falls der IdP offline ist. |

---

## 4. Technische Schnittstellen & Architektur

### OIDC-Ablaufdiagramm
```
[User] ──► Klick "SSO Login" ──► [Go Backend: /api/auth/oidc/login]
  ▲                                            │
  │                                            ▼ (Redirection)
  └────────────────────────────── [Identity Provider (Keycloak/Okta)]
                                               │
                                               ▼ (Callback with Code)
[User] ◄── Login erfolgreich ◄── [Go Backend: /api/auth/oidc/callback]
```

### OIDC-Integration im Go-Backend
*   **Bibliothek:** Verwendung des offiziellen Pakets `golang.org/x/oauth2` und `github.com/coreos/go-oidc/v3/oidc` zur Validierung des ID-Tokens und Signaturprüfung.
*   **Architektur-Regel (Key Segregation):** Die OIDC-Client-Secrets werden separat im Environment verwaltet. Nach erfolgreichem Login generiert das Backend ein JWT-Token unter Verwendung des dedizierten `JWT_SECRET_KEY` zur Session-Validierung im Frontend.

```go
// Callback-Handler im API-Gateway
func HandleOIDCCallback(w http.ResponseWriter, r *http.Request) {
	code := r.URL.Query().Get("code")
	oauth2Token, err := oauth2Config.Exchange(r.Context(), code)
	if err != nil {
		http.Error(w, "Failed to exchange token", http.StatusInternalServerError)
		return
	}

	rawIDToken, ok := oauth2Token.Extra("id_token").(string)
	if !ok {
		http.Error(w, "No id_token in token response", http.StatusBadRequest)
		return
	}

	// ID-Token signaturtechnisch validieren
	idToken, err := oidcVerifier.Verify(r.Context(), rawIDToken)
	if err != nil {
		http.Error(w, "Failed to verify ID token", http.StatusUnauthorized)
		return
	}

	// Claims auslesen
	var claims struct {
		Subject string `json:"sub"`
		Email   string `json:"email"`
		Name    string `json:"name"`
	}
	if err := idToken.Claims(&claims); err != nil {
		http.Error(w, "Failed to parse claims", http.StatusInternalServerError)
		return
	}

	// Benutzer in lokaler DB abgleichen / erstellen
	user, err := db.GetOrCreateUserByOIDCSub(claims.Subject, claims.Email, claims.Name)
	
	// Generiere JWT-Token für Frontend-Sitzung
	sessionToken, _ := crypto.GenerateJWT(user.ID, jwtSecretKey)
	
	// Weiterleitung ans Frontend mit Session-Token
	http.Redirect(w, r, fmt.Sprintf("%s/login/success?token=%s", frontendURL, sessionToken), http.StatusFound)
}
```

---

## 5. Sicherheits- und Datenschutzaspekte (DSGVO)
*   **Eindeutige Identifikation:** Das Feld `sub` (Subject) des IdPs dient als Primärschlüssel zur Zuordnung. E-Mail-Adressen werden nur zur Information und Zuordnung genutzt.
*   **Token-Sicherheit:** OIDC Access- und ID-Tokens werden nicht dauerhaft gespeichert, sondern flüchtig zur Sitzungserstellung genutzt. Nur das lokal generierte Session-JWT verbleibt beim Client.

---

## 6. Akzeptanzkriterien
1. Der Benutzer wird nach Klick auf "SSO Login" zum konfigurierten Identity Provider umgeleitet.
2. Nach erfolgreichem Login dort wird er zurückgeleitet, automatisch eingeloggt und im Frontend personalisiert begrüßt (Name aus OIDC Claim).
3. Nicht verifizierte ID-Tokens (z. B. manipulierter Signatur-Schlüssel) werden vom Backend abgewiesen.
4. Alle erstellten Ressourcen (Migrationen, Schedules) werden der neu erstellten lokalen `UserID` fest zugeordnet (Isolation).
