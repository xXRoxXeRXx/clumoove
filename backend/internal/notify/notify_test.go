package notify

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"unicode/utf8"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (fn roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return fn(req)
}

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

func TestSMTPConfigNormalizesOnlyMissingOrZeroPort(t *testing.T) {
	password := "  password with spaces  "
	if got := smtpConfig(Config{"smtp_password": password, "smtp_port": 0}); got.Password != password || got.Port != "587" {
		t.Fatalf("smtpConfig() = %#v, want unchanged password and default port", got)
	}
	if got := smtpConfig(Config{"smtp_port": "465"}).Port; got != "465" {
		t.Fatalf("smtpConfig() port = %q, want 465", got)
	}
}

func TestValidate(t *testing.T) {
	tests := []struct {
		name    string
		typ     string
		cfg     Config
		wantErr error
	}{
		{name: "email", typ: "email", cfg: Config{}},
		{name: "telegram", typ: "telegram", cfg: Config{"bot_token": "token", "chat_id": "42"}},
		{name: "missing telegram chat", typ: "telegram", cfg: Config{"bot_token": "token"}, wantErr: ErrIncomplete},
		{name: "invalid channel", typ: "pager", cfg: Config{}, wantErr: ErrInvalidChannel},
		{name: "gotify missing token", typ: "gotify", cfg: Config{"url": "https://8.8.8.8"}, wantErr: ErrIncomplete},
		{name: "gotify", typ: "gotify", cfg: Config{"url": "https://8.8.8.8", "token": "token"}},
		{name: "ntfy", typ: "ntfy", cfg: Config{"url": "https://8.8.8.8", "topic": "topic", "priority": "5"}},
		{name: "ntfy invalid priority", typ: "ntfy", cfg: Config{"url": "https://8.8.8.8", "topic": "topic", "priority": "6"}, wantErr: ErrInvalidPriority},
		{name: "discord", typ: "discord", cfg: Config{"webhook_url": "https://8.8.8.8/hook"}},
		{name: "discord blocked URL", typ: "discord", cfg: Config{"webhook_url": "http://127.0.0.1/hook"}, wantErr: ErrURLBlocked},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := Validate(tt.typ, tt.cfg)
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("Validate() error = %v, want errors.Is(_, %v)", err, tt.wantErr)
			}
		})
	}
}

func TestSendBuildsDiscordRequestWithExactIntegerCounts(t *testing.T) {
	previous := newEgressHTTPClient
	t.Cleanup(func() { newEgressHTTPClient = previous })
	var gotURL string
	var gotBody []byte
	newEgressHTTPClient = func(rawURL string) (*http.Client, error) {
		gotURL = rawURL
		return &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			gotBody, _ = io.ReadAll(req.Body)
			return &http.Response{StatusCode: http.StatusNoContent, Body: io.NopCloser(strings.NewReader("")), Header: make(http.Header), Request: req}, nil
		})}, nil
	}
	payload := json.RawMessage(`{"kind":"sync","name":"Nightly","status":"COMPLETED","processed":9007199254740993,"total":9007199254740994,"failed":1,"skipped":2}`)
	if err := Send(context.Background(), "discord", Config{"webhook_url": "https://example.test/hook"}, payload, "", "en"); err != nil {
		t.Fatalf("Send() error = %v", err)
	}
	if gotURL != "https://example.test/hook" {
		t.Fatalf("egress URL = %q", gotURL)
	}
	if !bytes.Contains(gotBody, []byte("9007199254740993 / 9007199254740994")) {
		t.Fatalf("request body = %s, want exact integer counts", gotBody)
	}
}

func TestSendSetsValidatedNtfyPriorityHeader(t *testing.T) {
	previous := newEgressHTTPClient
	t.Cleanup(func() { newEgressHTTPClient = previous })
	var gotPriority string
	newEgressHTTPClient = func(string) (*http.Client, error) {
		return &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			gotPriority = req.Header.Get("Priority")
			return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader("")), Header: make(http.Header), Request: req}, nil
		})}, nil
	}
	payload := json.RawMessage(`{"kind":"sync","name":"Nightly","status":"COMPLETED","processed":3,"total":4,"failed":1,"skipped":2}`)
	err := Send(context.Background(), "ntfy", Config{"url": "https://example.test", "topic": "backups", "priority": "4"}, payload, "", "en")
	if err != nil {
		t.Fatalf("Send() error = %v", err)
	}
	if gotPriority != "4" {
		t.Fatalf("Priority header = %q, want 4", gotPriority)
	}
}

func TestSendRejectsMalformedPayloadBeforeDelivery(t *testing.T) {
	previous := newEgressHTTPClient
	t.Cleanup(func() { newEgressHTTPClient = previous })
	called := false
	newEgressHTTPClient = func(string) (*http.Client, error) {
		called = true
		return nil, errors.New("must not be called")
	}
	err := Send(context.Background(), "discord", Config{"webhook_url": "https://example.test/hook"}, json.RawMessage(`{"kind":`), "", "en")
	if !errors.Is(err, ErrInvalidPayload) {
		t.Fatalf("Send() error = %v, want invalid payload", err)
	}
	if called {
		t.Fatal("Send() constructed an egress client for malformed payload")
	}
}

func TestDecodePayloadRejectsIncompleteOrInvalidValues(t *testing.T) {
	tests := []struct {
		name    string
		raw     string
		wantErr error
	}{
		{name: "null", raw: "null", wantErr: ErrInvalidPayload},
		{name: "empty", raw: "", wantErr: ErrInvalidPayload},
		{name: "whitespace", raw: " ", wantErr: ErrInvalidPayload},
		{name: "empty object", raw: "{}", wantErr: ErrInvalidPayload},
		{name: "missing status", raw: `{"kind":"sync"}`, wantErr: ErrInvalidPayload},
		{name: "fractional count", raw: `{"kind":"sync","status":"COMPLETED","processed":1.5}`, wantErr: ErrInvalidPayload},
		{name: "valid", raw: `{"kind":"sync","status":"COMPLETED"}`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := decodePayload(json.RawMessage(tt.raw))
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("decodePayload(%q) error = %v, want errors.Is(_, %v)", tt.raw, err, tt.wantErr)
			}
		})
	}
}

func TestFormatLocalizedAndSubject(t *testing.T) {
	payload, err := decodePayload(json.RawMessage(`{"kind":"sync","name":"Nightly","status":"COMPLETED","processed":3,"total":4,"failed":1,"skipped":2}`))
	if err != nil {
		t.Fatalf("decodePayload() error = %v", err)
	}
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
	for _, max := range []int{-1, 0, 1, 2} {
		if got := truncate("long value", max); got != "long value" {
			t.Fatalf("truncate(max=%d) = %q, want unchanged", max, got)
		}
	}
}
