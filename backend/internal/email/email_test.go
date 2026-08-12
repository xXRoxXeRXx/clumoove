package email

import (
	"context"
	"errors"
	"net"
	"strings"
	"sync"
	"testing"
)

var resolveSMTPIPsTestMu sync.Mutex

func TestValidateSMTPHost(t *testing.T) {
	withSMTPResolver(t, func(_ context.Context, host string) ([]net.IP, error) {
		switch host {
		case "private.example":
			return []net.IP{net.ParseIP("10.0.0.5")}, nil
		case "mixed.example":
			return []net.IP{net.ParseIP("203.0.113.10"), net.ParseIP("192.168.1.10")}, nil
		case "cgnat.example":
			return []net.IP{net.ParseIP("100.64.0.1")}, nil
		case "benchmark.example":
			return []net.IP{net.ParseIP("198.18.0.1")}, nil
		case "error.example":
			return nil, errors.New("DNS unavailable")
		case "empty.example":
			return nil, nil
		default:
			return []net.IP{net.ParseIP("203.0.113.10")}, nil
		}
	})

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
		{name: "CGNAT DNS answer", host: "cgnat.example", wantErr: "private or internal"},
		{name: "benchmark DNS answer", host: "benchmark.example", wantErr: "private or internal"},
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
	lookups := 0
	withSMTPResolver(t, func(context.Context, string) ([]net.IP, error) {
		lookups++
		if lookups == 1 {
			return []net.IP{net.ParseIP("203.0.113.10")}, nil
		}
		return []net.IP{net.ParseIP("10.0.0.5")}, nil
	})

	err := SendMail(SMTPConfig{Host: "smtp.example", Port: "25", Encryption: "starttls"}, "recipient@example.com", "subject", "body")
	if err == nil || !strings.Contains(err.Error(), "private or internal address") {
		t.Fatalf("expected rebinding to be rejected with private-address error, got: %v", err)
	}
	if lookups != 2 {
		t.Fatalf("expected host to be resolved before validation and dialing, got %d lookups", lookups)
	}
}

func TestSanitizeEmailContentRemovesSMTPControlCharacters(t *testing.T) {
	got := sanitizeEmailContent(" \tHello\r\nBcc: attacker@example.com\x00\x7f ")
	if got != "HelloBcc: attacker@example.com" {
		t.Fatalf("sanitizeEmailContent() = %q", got)
	}
}

func TestNormalizeMailboxHeaderCanonicalizesAddress(t *testing.T) {
	got := normalizeMailboxHeader("Clumoove Support <support@example.com>")
	if got != `"Clumoove Support" <support@example.com>` {
		t.Fatalf("normalizeMailboxHeader() = %q", got)
	}
}

func TestNormalizeEnvelopeAddressReturnsMailboxOnly(t *testing.T) {
	got := normalizeEnvelopeAddress("Clumoove Support <support@example.com>")
	if got != "support@example.com" {
		t.Fatalf("normalizeEnvelopeAddress() = %q", got)
	}
}

func TestNormalizeMailboxAddressFallbackSanitizesUnparseableValue(t *testing.T) {
	input := "not an address\r\nBcc: attacker@example.com"
	want := "not an addressBcc: attacker@example.com"

	if got := normalizeMailboxHeader(input); got != want {
		t.Fatalf("normalizeMailboxHeader() = %q, want %q", got, want)
	}
	if got := normalizeEnvelopeAddress(input); got != want {
		t.Fatalf("normalizeEnvelopeAddress() = %q, want %q", got, want)
	}
}

func TestValidateSMTPPort(t *testing.T) {
	for _, port := range []string{"", "0", "65536", "smtp"} {
		if err := validateSMTPPort(port); err == nil {
			t.Fatalf("validateSMTPPort(%q) succeeded, want error", port)
		}
	}
	if err := validateSMTPPort("587"); err != nil {
		t.Fatalf("validateSMTPPort(\"587\") error = %v", err)
	}
}

func TestSendMailRejectsUnsupportedEncryption(t *testing.T) {
	withSMTPResolver(t, func(context.Context, string) ([]net.IP, error) {
		return []net.IP{net.ParseIP("203.0.113.10")}, nil
	})

	err := SendMail(SMTPConfig{Host: "smtp.example", Port: "587", Encryption: "none"}, "recipient@example.com", "subject", "body")
	if err == nil || !strings.Contains(err.Error(), "unsupported SMTP encryption") {
		t.Fatalf("SendMail() error = %v, want unsupported encryption error", err)
	}
}

func TestSendMailContextHonorsCancellation(t *testing.T) {
	withSMTPResolver(t, func(context.Context, string) ([]net.IP, error) {
		return []net.IP{net.ParseIP("203.0.113.10")}, nil
	})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := SendMailContext(ctx, SMTPConfig{Host: "smtp.example", Port: "587", Encryption: "starttls"}, "recipient@example.com", "subject", "body")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("SendMailContext() error = %v, want context.Canceled", err)
	}
}

func withSMTPResolver(t *testing.T, resolver func(context.Context, string) ([]net.IP, error)) {
	t.Helper()
	resolveSMTPIPsTestMu.Lock()
	resolveSMTPIPsMu.Lock()
	originalResolver := resolveSMTPIPs
	resolveSMTPIPs = resolver
	resolveSMTPIPsMu.Unlock()
	t.Cleanup(func() {
		resolveSMTPIPsMu.Lock()
		resolveSMTPIPs = originalResolver
		resolveSMTPIPsMu.Unlock()
		resolveSMTPIPsTestMu.Unlock()
	})
}

func TestBuildMessagePreventsSMTPMessageInjection(t *testing.T) {
	message := buildMessage(
		"Clumoove\r\nBcc: attacker@example.com <no-reply@example.com>",
		"recipient@example.com\r\nBcc: attacker@example.com",
		"Status\r\nBcc: attacker@example.com",
		"Hello\r\n\r\nBcc: attacker@example.com\x00",
	)

	if strings.Contains(message, "\r\nBcc:") {
		t.Fatalf("buildMessage() allowed an injected header: %q", message)
	}
	if !strings.Contains(message, "To: recipient@example.comBcc: attacker@example.com\r\n") {
		t.Fatalf("buildMessage() did not sanitize the recipient header: %q", message)
	}
	if !strings.Contains(message, "Subject: StatusBcc: attacker@example.com\r\n") {
		t.Fatalf("buildMessage() did not preserve the sanitized subject: %q", message)
	}
}

func TestLocalizedActionEmailUsesResponsiveBrandedShell(t *testing.T) {
	body := BuildEmailChangeEmailLocalized("https://clumoove.com/confirm?token=very-long-token", "name<script>@example.com", "en")

	for _, want := range []string{
		`<meta name="viewport" content="width=device-width, initial-scale=1.0">`,
		`https://clumoove.com/clumoove_logo.svg`,
		`alt="Clumoove"`,
		`@media screen and (max-width: 640px)`,
		`.cta-link { display: block !important; width: 100% !important;`,
		`class="cta-link"`,
		`name&lt;script&gt;@example.com`,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("action email missing %q", want)
		}
	}
	if strings.Contains(body, "name<script>@example.com") {
		t.Fatalf("action email included unescaped dynamic email: %q", body)
	}
}

func TestLocalizedEmailSetsDocumentLanguage(t *testing.T) {
	if body := BuildTestEmailLocalized("de"); !strings.Contains(body, `<html lang="de">`) {
		t.Fatalf("German email did not set lang=de: %q", body)
	}
	if body := BuildTestEmailLocalized("fr"); !strings.Contains(body, `<html lang="en">`) {
		t.Fatalf("fallback email did not set lang=en: %q", body)
	}
}

func TestLocalizedNotificationEmailEscapesResponsiveSummary(t *testing.T) {
	body := BuildNotificationEmailLocalized("migration", `Project <script>alert(1)</script>`, `<failed>`, "1", "2", "0", "0", "en")

	for _, want := range []string{
		`class="summary-label"`,
		`class="summary-value"`,
		`overflow-wrap:anywhere`,
		`Project &lt;script&gt;alert(1)&lt;/script&gt;`,
		`&lt;failed&gt;`,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("notification email missing %q", want)
		}
	}
	if strings.Contains(body, "Project <script>") || strings.Contains(body, "<failed>") {
		t.Fatalf("notification email included unescaped dynamic content: %q", body)
	}
}
