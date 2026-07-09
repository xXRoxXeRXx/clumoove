# PRD 20: Namenskonvertierung & Dateinamen-Bereinigung

## 1. Einleitung & Ziel
Unterschiedliche Speicher-Provider und Betriebssysteme haben inkompatible Einschränkungen für Datei- und Ordnernamen. 
*   *Windows/SMB* verbietet Zeichen wie `:`, `*`, `?`, `"`, `<` oder `>`.
*   *Linux/Nextcloud* ist case-sensitive (Groß-/Kleinschreibung wird unterschieden: `Foto.jpg` und `foto.jpg` können im selben Ordner liegen).
*   *OneDrive/Dropbox* sind case-insensitive (Kollision!).

Das Ziel dieses Features ist es, Inkompatibilitäten bei Dateinamen vor oder während des Transfers automatisch zu bereinigen, Namenskollisionen intelligent aufzulösen und die Änderungen für den Benutzer transparent zu protokollieren.

---

## 2. Anwendungsfälle (Use Cases)
*   **UC-1 (Kollision durch Groß-/Kleinschreibung):** Ein Linux-SFTP-Verzeichnis enthält `Rechnung.pdf` und `rechnung.pdf`. Das Ziel ist eine Windows-SMB-Freigabe. Das System erkennt die Kollision und benennt die zweite Datei automatisch in `rechnung_1.pdf` um.
*   **UC-2 (Ungültige Zeichen entfernen):** Eine Datei in Nextcloud heißt `Bericht: 2026.pdf`. Das Ziel-System ist ein lokales Windows-Laufwerk. Der Doppelpunkt `:` ist dort verboten. Das System benennt die Datei beim Transfer automatisch in `Bericht_ 2026.pdf` um.
*   **UC-3 (Reservierte Namen):** Eine Datei heißt `aux.txt` (in Windows ein reservierter Name für Geräte). Beim Upload auf ein Windows-Ziel wird sie in `_aux.txt` umbenannt.

---

## 3. Funktionale Anforderungen

| ID | Anforderung | Priorität | Beschreibung |
| :--- | :--- | :--- | :--- |
| **F-01** | Automatische Bereinigung | MUST | Ersetzung verbotener Zeichen (z.B. `/`, `\`, `:`, `*`, `?`, `"`, `<`, `>`, `|`) durch einen konfigurierbaren Platzhalter (Standard: `_`). |
| **F-02** | Case-Insensitive Kollisionsprüfung | MUST | Scannen des Ziel-Verzeichnisses und Erkennung, ob bereits eine Datei existiert, die sich nur in der Groß-/Kleinschreibung unterscheidet, um Datenüberschreibungen zu verhindern. |
| **F-03** | Längen-Kürzung | MUST | Automatisches Kürzen von Dateinamen bei Überschreitung der maximalen Pfadlänge (z. B. max. 255 Zeichen für den Namen oder 260 Zeichen für den Gesamtpfad), unter Beibehaltung der Dateiendung (z.B. `.docx`). |
| **F-04** | Protokollierung | MUST | Jede Namensänderung muss in der Spalte `error_message` der Tabelle `tasks` (als Info-Log) und im finalen Bericht dokumentiert werden (z. B. *"Umgelautet von A zu B"*). |
| **F-05** | Benutzerdefinierte Regeln | SHOULD | Der Benutzer kann im Frontend eigene Regeln festlegen (z. B. *Umlaute ersetzen ä->ae*, *Leerzeichen durch Bindestriche ersetzen*). |

---

## 4. Technische Schnittstellen & Architektur

### Namensbereinigungs-Engine in Go
Vor der Übermittlung eines Tasks an den Worker wird der Zielpfad durch die Sanitization-Bibliothek geleitet:

```go
func SanitizeFilename(name string, targetProvider string) string {
	// 1. Verbotene Zeichen je nach Provider ersetzen
	forbiddenChars := `[\/\?%\*:\|"<> ]` // Standard-Regex für kritische Zeichen
	if targetProvider == "smb" || targetProvider == "local_windows" {
		reg := regexp.MustCompile(forbiddenChars)
		name = reg.ReplaceAllString(name, "_")
	}

	// 2. Windows-Sondernamen schützen (CON, PRN, AUX, NUL, etc.)
	reservedNames := []string{"CON", "PRN", "AUX", "NUL", "COM1", "LPT1"}
	baseName := strings.ToUpper(strings.TrimSuffix(name, filepath.Ext(name)))
	for _, r := range reservedNames {
		if baseName == r {
			name = "_" + name
			break
		}
	}

	// 3. Maximale Länge kürzen (z. B. auf 255 Zeichen)
	ext := filepath.Ext(name)
	if len(name) > 255 {
		name = name[:255-len(ext)] + ext
	}

	return name
}
```

---

## 5. Sicherheits- und Datenschutzaspekte (DSGVO)
*   **Keine Beeinträchtigung:** Die Namensänderung erfolgt flüchtig bei der Generierung des Task-Eintrags im Backend. Es werden keine Original-Dateien auf dem Quell-System verändert.

---

## 6. Akzeptanzkriterien
1. Dateien mit Sonderzeichen (`Bericht:2026.pdf`) werden beim Upload auf ein restriktives Zielsystem (z. B. SMB) automatisch in bereinigter Form (`Bericht_2026.pdf`) angelegt.
2. Bei Case-Sensitive Kollisionsdateien im selben Quellordner (`file.txt` und `File.txt`) wird die zweite Datei auf einem Case-Insensitive Zielsystem als `File_1.txt` abgelegt.
3. Die Protokolldaten in der Datenbank weisen die Namensänderungen eindeutig aus, sodass der Benutzer die Datei auf dem Zielsystem wiederfinden kann.
