// Package i18n provides the backend view of the frontend translation catalog.
package i18n

//go:generate go run ./cmd/generate

import "strings"

func T(language, key string) string {
	if language != "de" {
		language = "en"
	}
	if value := translations[language][key]; value != "" {
		return value
	}
	return translations["en"][key]
}

func sanitizeReplacementValue(value string) string {
	return strings.NewReplacer("\r", "", "\n", "", "\x00", "").Replace(value)
}

func Format(language, key string, values map[string]string) string {
	value := T(language, key)
	for name, replacement := range values {
		value = strings.ReplaceAll(value, "{{"+name+"}}", sanitizeReplacementValue(replacement))
	}
	return value
}
