package i18n

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
)

func TestTUsesRequestedLanguageAndFallsBackToEnglish(t *testing.T) {
	if got := T("de", "delivery.notification.processed"); got != "Verarbeitet" {
		t.Fatalf("German translation = %q, want %q", got, "Verarbeitet")
	}
	if got := T("fr", "delivery.notification.processed"); got != "Processed" {
		t.Fatalf("fallback translation = %q, want %q", got, "Processed")
	}
	if got := T("DE", "delivery.notification.processed"); got != "Verarbeitet" {
		t.Fatalf("case-insensitive German translation = %q, want %q", got, "Verarbeitet")
	}
	if got := T("en", "missing.key"); got != "" {
		t.Fatalf("unknown translation = %q, want empty string", got)
	}
}

func TestTFallsBackToEnglishForMissingRequestedLanguageKey(t *testing.T) {
	// Do not run with t.Parallel(): this test mutates the shared translations map.
	const key = "test.fallback"
	translations["en"][key] = "English fallback"
	t.Cleanup(func() { delete(translations["en"], key) })

	if got := T("de", key); got != "English fallback" {
		t.Fatalf("German fallback translation = %q, want %q", got, "English fallback")
	}
}

func TestTPreservesEmptyTranslationValues(t *testing.T) {
	// Do not run with t.Parallel(): this test mutates the shared translations map.
	const key = "test.empty"
	translations["de"][key] = ""
	translations["en"][key] = "English fallback"
	t.Cleanup(func() {
		delete(translations["de"], key)
		delete(translations["en"], key)
	})

	if got := T("de", key); got != "" {
		t.Fatalf("empty German translation = %q, want empty string", got)
	}
}

func TestFormatReplacesNamedPlaceholders(t *testing.T) {
	got := Format("en", "delivery.emailChange.message", map[string]string{"email": "person@example.com"})
	want := "You requested to change your email address to <strong>person@example.com</strong>."
	if got != want {
		t.Fatalf("Format() = %q, want %q", got, want)
	}
}

func TestFormatReplacesWhitespacePaddedPlaceholders(t *testing.T) {
	// Do not run with t.Parallel(): this test mutates the shared translations map.
	const key = "test.whitespacePlaceholder"
	translations["en"][key] = "Hello, {{ name }}!"
	t.Cleanup(func() { delete(translations["en"], key) })

	if got := Format("en", key, map[string]string{"name": "person@example.com"}); got != "Hello, person@example.com!" {
		t.Fatalf("Format() = %q, want whitespace-padded placeholder to be replaced", got)
	}
}

func TestFormatSanitizesReplacementValues(t *testing.T) {
	got := Format("en", "delivery.emailChange.message", map[string]string{"email": "user@example.com\r\nBcc: attacker@example.com\x00"})
	if strings.ContainsAny(got, "\r\n\x00") {
		t.Fatalf("Format() retained SMTP control characters: %q", got)
	}
}

func TestGeneratedCatalogMatchesFrontendLocales(t *testing.T) {
	for _, language := range []string{"de", "en"} {
		expected := loadFrontendDeliveryCatalog(t, language)
		if !reflect.DeepEqual(translations[language], expected) {
			t.Errorf("generated %s catalog differs from frontend source\n%s", language, catalogDiff(translations[language], expected))
		}
	}
}

func loadFrontendDeliveryCatalog(t *testing.T, language string) map[string]string {
	t.Helper()
	path := filepath.Join("..", "..", "..", "frontend", "src", "locales", language, "translation.json")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read frontend %s catalog: %v", language, err)
	}

	var document map[string]any
	if err := json.Unmarshal(raw, &document); err != nil {
		t.Fatalf("parse frontend %s catalog: %v", language, err)
	}
	delivery, ok := document["delivery"].(map[string]any)
	if !ok {
		t.Fatalf("frontend %s catalog has no delivery object", language)
	}

	result := make(map[string]string)
	flattenFrontendCatalog(t, "delivery.", delivery, result)
	return result
}

func flattenFrontendCatalog(t *testing.T, prefix string, value any, out map[string]string) {
	t.Helper()
	switch value := value.(type) {
	case map[string]any:
		for key, child := range value {
			flattenFrontendCatalog(t, prefix+key+".", child, out)
		}
	case string:
		out[strings.TrimSuffix(prefix, ".")] = value
	default:
		t.Fatalf("frontend catalog value at %q must be a string or object, got %T", strings.TrimSuffix(prefix, "."), value)
	}
}

func catalogDiff(got, want map[string]string) string {
	var differences []string
	for key, value := range want {
		if gotValue, ok := got[key]; !ok {
			differences = append(differences, "missing generated key: "+key)
		} else if gotValue != value {
			differences = append(differences, "different generated value: "+key)
		}
	}
	for key := range got {
		if _, ok := want[key]; !ok {
			differences = append(differences, "unexpected generated key: "+key)
		}
	}
	sort.Strings(differences)
	return strings.Join(differences, "\n")
}
