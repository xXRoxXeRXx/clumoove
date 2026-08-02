package i18n

import (
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
	if got := T("en", "missing.key"); got != "" {
		t.Fatalf("unknown translation = %q, want empty string", got)
	}
}

func TestFormatReplacesNamedPlaceholders(t *testing.T) {
	got := Format("en", "delivery.emailChange.message", map[string]string{"email": "person@example.com"})
	want := "You requested to change your email address to <strong>person@example.com</strong>."
	if got != want {
		t.Fatalf("Format() = %q, want %q", got, want)
	}
}

func TestFormatSanitizesReplacementValues(t *testing.T) {
	got := Format("en", "delivery.emailChange.message", map[string]string{"email": "user@example.com\r\nBcc: attacker@example.com\x00"})
	if strings.ContainsAny(got, "\r\n\x00") {
		t.Fatalf("Format() retained SMTP control characters: %q", got)
	}
}
