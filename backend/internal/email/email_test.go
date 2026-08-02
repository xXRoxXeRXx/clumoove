package email

import (
	"context"
	"errors"
	"net"
	"strings"
	"testing"
)

func TestValidateSMTPHost(t *testing.T) {
	originalResolver := resolveSMTPIPs
	defer func() { resolveSMTPIPs = originalResolver }()

	resolveSMTPIPs = func(_ context.Context, host string) ([]net.IP, error) {
		switch host {
		case "private.example":
			return []net.IP{net.ParseIP("10.0.0.5")}, nil
		case "mixed.example":
			return []net.IP{net.ParseIP("203.0.113.10"), net.ParseIP("192.168.1.10")}, nil
		case "error.example":
			return nil, errors.New("DNS unavailable")
		case "empty.example":
			return nil, nil
		default:
			return []net.IP{net.ParseIP("203.0.113.10")}, nil
		}
	}

	tests := []struct {
		name    string
		host    string
		wantErr string
	}{
		{name: "public literal", host: "203.0.113.10"},
		{name: "public hostname", host: "public.example"},
		{name: "private literal", host: "10.0.0.5", wantErr: "private or internal"},
		{name: "private DNS answer", host: "private.example", wantErr: "private or internal"},
		{name: "mixed DNS answers", host: "mixed.example", wantErr: "private or internal"},
		{name: "DNS lookup failure", host: "error.example", wantErr: "lookup failed"},
		{name: "empty DNS answers", host: "empty.example", wantErr: "no addresses"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateSMTPHost(tt.host)
			if tt.wantErr == "" && err != nil {
				t.Fatalf("ValidateSMTPHost(%q) error = %v, want no error", tt.host, err)
			}
			if tt.wantErr != "" && (err == nil || !strings.Contains(err.Error(), tt.wantErr)) {
				t.Fatalf("ValidateSMTPHost(%q) error = %v, want error containing %q", tt.host, err, tt.wantErr)
			}
		})
	}
}

func TestSendMailRejectsReboundPrivateAddress(t *testing.T) {
	originalResolver := resolveSMTPIPs
	defer func() { resolveSMTPIPs = originalResolver }()

	lookups := 0
	resolveSMTPIPs = func(context.Context, string) ([]net.IP, error) {
		lookups++
		if lookups == 1 {
			return []net.IP{net.ParseIP("203.0.113.10")}, nil
		}
		return []net.IP{net.ParseIP("10.0.0.5")}, nil
	}

	err := SendMail(SMTPConfig{Host: "smtp.example", Port: "25"}, "recipient@example.com", "subject", "body")
	if err == nil || !strings.Contains(err.Error(), "private or internal address") {
		t.Fatalf("expected rebinding to be rejected with private-address error, got: %v", err)
	}
	if lookups != 2 {
		t.Fatalf("expected host to be resolved before validation and dialing, got %d lookups", lookups)
	}
}

func TestSanitizeEmailContentRemovesSMTPControlCharacters(t *testing.T) {
	got := sanitizeEmailContent("Hello\r\nBcc: attacker@example.com\x00")
	if got != "HelloBcc: attacker@example.com" {
		t.Fatalf("sanitizeEmailContent() = %q", got)
	}
}

func TestBuildMessagePreventsSMTPMessageInjection(t *testing.T) {
	message := buildMessage(
		"Clumoove <no-reply@example.com>",
		"recipient@example.com",
		"Status\r\nBcc: attacker@example.com",
		"Hello\r\n\r\nBcc: attacker@example.com\x00",
	)

	if strings.Contains(message, "\r\nBcc:") {
		t.Fatalf("buildMessage() allowed an injected header: %q", message)
	}
	if !strings.Contains(message, "Subject: StatusBcc: attacker@example.com\r\n") {
		t.Fatalf("buildMessage() did not preserve the sanitized subject: %q", message)
	}
}
