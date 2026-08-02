package notify

import (
	"encoding/json"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestSanitizeSMTPValue(t *testing.T) {
	got := sanitizeSMTPValue(" \r\nsmtp.example\x00 \n")
	if got != "smtp.example" {
		t.Fatalf("sanitizeSMTPValue() = %q", got)
	}
}

func TestSanitizeSMTPAddressValue(t *testing.T) {
	if got := sanitizeSMTPAddressValue("Sender <sender@example.com>"); got != "sender@example.com" {
		t.Fatalf("sanitizeSMTPAddressValue() = %q", got)
	}
	if got := sanitizeSMTPAddressValue("not an address"); got != "" {
		t.Fatalf("sanitizeSMTPAddressValue() = %q, want empty", got)
	}
}

func TestSMTPPasswordIsNotNormalized(t *testing.T) {
	password := "  password with spaces  "
	if got := smtpConfig(Config{"smtp_password": password}).Password; got != password {
		t.Fatalf("SMTP password = %q, want %q", got, password)
	}
}

func TestValidate(t *testing.T) {
	tests := []struct {
		name    string
		typ     string
		cfg     Config
		wantErr string
	}{
		{name: "telegram", typ: "telegram", cfg: Config{"bot_token": "token", "chat_id": "42"}},
		{name: "missing telegram chat", typ: "telegram", cfg: Config{"bot_token": "token"}, wantErr: "incomplete"},
		{name: "invalid channel", typ: "pager", cfg: Config{}, wantErr: "invalid channel"},
		{name: "gotify missing token", typ: "gotify", cfg: Config{"url": "https://8.8.8.8"}, wantErr: "incomplete"},
		{name: "discord blocked URL", typ: "discord", cfg: Config{"webhook_url": "http://127.0.0.1/hook"}, wantErr: ErrURLBlocked.Error()},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := Validate(tt.typ, tt.cfg)
			if tt.wantErr == "" && err != nil {
				t.Fatalf("Validate() error = %v, want nil", err)
			}
			if tt.wantErr != "" && (err == nil || !strings.Contains(err.Error(), tt.wantErr)) {
				t.Fatalf("Validate() error = %v, want %v", err, tt.wantErr)
			}
		})
	}
}

func TestFormatLocalizedAndSubject(t *testing.T) {
	payload := json.RawMessage(`{"kind":"sync","name":"Nightly","status":"COMPLETED","processed":3,"total":4,"failed":1,"skipped":2}`)
	got := formatLocalized(payload, "en")
	want := "Sync Nightly\nStatus: COMPLETED\nProcessed: 3 / 4\nFailed: 1\nSkipped: 2"
	if got != want {
		t.Fatalf("formatLocalized() = %q, want %q", got, want)
	}
	if got := notificationSubject("fr"); got != "Clumoove notification" {
		t.Fatalf("fallback subject = %q, want English subject", got)
	}
}

func TestTruncatePreservesUTF8Runes(t *testing.T) {
	value := strings.Repeat("ä", 6)
	got := truncate(value, 5)
	if got != "ää..." {
		t.Fatalf("truncate() = %q, want %q", got, "ää...")
	}
	if !utf8.ValidString(got) || utf8.RuneCountInString(got) != 5 {
		t.Fatalf("truncate() returned invalid or incorrectly sized UTF-8: %q", got)
	}
	if got := truncate("test", 5); got != "test" {
		t.Fatalf("short value = %q, want unchanged", got)
	}
}
