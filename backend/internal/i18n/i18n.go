// Package i18n provides the backend view of the frontend translation catalog.
package i18n

//go:generate go run ./cmd/generate

import (
	"log/slog"
	"regexp"
	"strings"
)

// T returns the translation for key in language. German is selected
// case-insensitively; all other languages fall back to English.
func T(language, key string) string {
	language = strings.ToLower(language)
	if language != "de" {
		language = "en"
	}
	if value, ok := translations[language][key]; ok {
		return value
	}
	return translations["en"][key]
}

var (
	sanitizeReplacementValues = strings.NewReplacer("\r", "", "\n", "", "\x00", "")
	placeholderPattern        = regexp.MustCompile(`\{\{\s*([^{}\s]+)\s*\}\}`)
)

func sanitizeReplacementValue(value string) string {
	return sanitizeReplacementValues.Replace(value)
}

// Format substitutes {{name}} placeholders in a translation. Replacement
// values have CR, LF, and NUL stripped to prevent SMTP header injection, but
// are not HTML-escaped. Callers placing values in HTML must escape them first
// (for example, with html.EscapeString), because templates may intentionally
// use placeholders inside HTML tag contexts.
func Format(language, key string, values map[string]string) string {
	value := T(language, key)
	debugEnabled := slog.Default().Enabled(nil, slog.LevelDebug)
	return placeholderPattern.ReplaceAllStringFunc(value, func(placeholder string) string {
		name := strings.TrimSpace(placeholder[2 : len(placeholder)-2])
		replacement, ok := values[name]
		if !ok {
			if debugEnabled {
				slog.Debug("i18n format placeholder is missing a replacement", "key", key, "placeholder", name)
			}
			return placeholder
		}
		return sanitizeReplacementValue(replacement)
	})
}
