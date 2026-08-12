package email

import (
	"context"
	"crypto/tls"
	"fmt"
	"html"
	"mime"
	"net"
	"net/mail"
	"net/smtp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"backend/internal/i18n"
)

type SMTPConfig struct {
	Host       string
	Port       string
	Username   string
	Password   string
	FromEmail  string
	FromName   string
	Encryption string // tls, starttls
}

const smtpOperationTimeout = 15 * time.Second

var (
	resolveSMTPIPs = func(ctx context.Context, host string) ([]net.IP, error) {
		return net.DefaultResolver.LookupIP(ctx, "ip", host)
	}
	resolveSMTPIPsMu sync.RWMutex
)

func lookupSMTPIPs(ctx context.Context, host string) ([]net.IP, error) {
	resolveSMTPIPsMu.RLock()
	resolver := resolveSMTPIPs
	resolveSMTPIPsMu.RUnlock()
	return resolver(ctx, host)
}

// ValidateSMTPHost rejects SMTP endpoints that resolve to a private or internal
// address. Unlike storage providers, SMTP must never reach private networks.
func ValidateSMTPHost(host string) error {
	ctx, cancel := context.WithTimeout(context.Background(), smtpOperationTimeout)
	defer cancel()
	return validateSMTPHost(ctx, host)
}

func validateSMTPHost(ctx context.Context, host string) error {
	if host == "" {
		return fmt.Errorf("SMTP host is required")
	}

	if ip := net.ParseIP(host); ip != nil {
		if !isAllowedSMTPIP(ip) {
			return fmt.Errorf("SMTP host must not be a private or internal IP address")
		}
		return nil
	}

	ips, err := lookupSMTPIPs(ctx, host)
	if err != nil {
		return fmt.Errorf("SMTP host lookup failed: %w", err)
	}
	if len(ips) == 0 {
		return fmt.Errorf("SMTP host resolved to no addresses")
	}
	for _, ip := range ips {
		if !isAllowedSMTPIP(ip) {
			return fmt.Errorf("SMTP host must not resolve to a private or internal address")
		}
	}
	return nil
}

func isAllowedSMTPIP(ip net.IP) bool {
	if !ip.IsGlobalUnicast() || ip.IsPrivate() {
		return false
	}

	// CGNAT and benchmarking ranges are non-public address space. Do not allow
	// the instance mailer to become a path into an internal network through
	// either range.
	if ipv4 := ip.To4(); ipv4 != nil {
		return !((ipv4[0] == 100 && ipv4[1] >= 64 && ipv4[1] <= 127) ||
			(ipv4[0] == 198 && (ipv4[1] == 18 || ipv4[1] == 19)))
	}
	return true
}

func SendMail(cfg SMTPConfig, to, subject, htmlBody string) error {
	ctx, cancel := context.WithTimeout(context.Background(), smtpOperationTimeout)
	defer cancel()

	if err := validateSMTPPort(cfg.Port); err != nil {
		return err
	}

	if err := validateSMTPHost(ctx, cfg.Host); err != nil {
		return err
	}

	from := cfg.FromEmail
	if cfg.FromName != "" {
		from = fmt.Sprintf("%s <%s>", cfg.FromName, cfg.FromEmail)
	}

	msg := buildMessage(from, to, subject, htmlBody)

	var auth smtp.Auth
	if cfg.Username != "" {
		auth = smtp.PlainAuth("", cfg.Username, cfg.Password, cfg.Host)
	}

	switch strings.ToLower(strings.TrimSpace(cfg.Encryption)) {
	case "tls":
		return sendWithTLS(ctx, cfg, auth, to, msg)
	case "starttls":
		return sendWithSTARTTLS(ctx, cfg, auth, to, msg)
	default:
		return fmt.Errorf("unsupported SMTP encryption %q", cfg.Encryption)
	}
}

func validateSMTPPort(port string) error {
	value, err := strconv.ParseUint(port, 10, 16)
	if err != nil || value == 0 {
		return fmt.Errorf("SMTP port must be between 1 and 65535")
	}
	return nil
}

// sanitizeEmailContent removes ASCII control characters from SMTP header and
// body values. HTML templates do not require those characters, so stripping
// them prevents header/message injection before MIME assembly.
func sanitizeEmailContent(value string) string {
	var b strings.Builder
	b.Grow(len(value))
	for _, r := range value {
		if r <= 0x1f || r == 0x7f {
			continue
		}
		b.WriteRune(r)
	}
	return strings.TrimSpace(b.String())
}

func smtpDialContext(ctx context.Context, host, port string) (net.Conn, error) {
	ips, err := lookupSMTPIPs(ctx, host)
	if err != nil {
		return nil, fmt.Errorf("SMTP host lookup failed: %w", err)
	}
	if len(ips) == 0 {
		return nil, fmt.Errorf("SMTP host resolved to no addresses")
	}
	for _, ip := range ips {
		if !isAllowedSMTPIP(ip) {
			return nil, fmt.Errorf("SMTP host resolved to a private or internal address")
		}
	}

	sort.SliceStable(ips, func(i, j int) bool {
		return ips[i].To4() != nil && ips[j].To4() == nil
	})

	var errMsgs []string
	for _, ip := range ips {
		network := "tcp6"
		if ip.To4() != nil {
			network = "tcp4"
		}
		conn, err := (&net.Dialer{Timeout: 5 * time.Second}).DialContext(ctx, network, net.JoinHostPort(ip.String(), port))
		if err == nil {
			return conn, nil
		}
		errMsgs = append(errMsgs, fmt.Sprintf("%s (%s): %v", ip, network, err))
	}
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("SMTP dial failed: %w", err)
	}
	return nil, fmt.Errorf("SMTP dial failed: %s", strings.Join(errMsgs, " | "))
}

func setSMTPDeadline(ctx context.Context, conn net.Conn) error {
	deadline, ok := ctx.Deadline()
	if !ok {
		return nil
	}
	return conn.SetDeadline(deadline)
}

func sendWithTLS(ctx context.Context, cfg SMTPConfig, auth smtp.Auth, to, msg string) error {
	tlsConfig := &tls.Config{
		ServerName: cfg.Host,
		MinVersion: tls.VersionTLS12,
	}

	conn, err := smtpDialContext(ctx, cfg.Host, cfg.Port)
	if err != nil {
		return fmt.Errorf("TLS dial failed: %w", err)
	}
	defer conn.Close()
	if err := setSMTPDeadline(ctx, conn); err != nil {
		return fmt.Errorf("TLS deadline failed: %w", err)
	}
	tlsConn := tls.Client(conn, tlsConfig)
	if err := tlsConn.HandshakeContext(ctx); err != nil {
		return fmt.Errorf("TLS handshake failed: %w", err)
	}

	client, err := smtp.NewClient(tlsConn, cfg.Host)
	if err != nil {
		return fmt.Errorf("SMTP client creation failed: %w", err)
	}
	defer client.Close()

	return sendSMTPMessage(client, cfg, auth, to, msg)
}

func sendWithSTARTTLS(ctx context.Context, cfg SMTPConfig, auth smtp.Auth, to, msg string) error {
	conn, err := smtpDialContext(ctx, cfg.Host, cfg.Port)
	if err != nil {
		return fmt.Errorf("dial failed: %w", err)
	}
	defer conn.Close()
	if err := setSMTPDeadline(ctx, conn); err != nil {
		return fmt.Errorf("SMTP deadline failed: %w", err)
	}

	client, err := smtp.NewClient(conn, cfg.Host)
	if err != nil {
		return fmt.Errorf("SMTP client creation failed: %w", err)
	}
	defer client.Close()

	tlsConfig := &tls.Config{
		ServerName: cfg.Host,
		MinVersion: tls.VersionTLS12,
	}
	if err := client.StartTLS(tlsConfig); err != nil {
		return fmt.Errorf("STARTTLS failed: %w", err)
	}

	return sendSMTPMessage(client, cfg, auth, to, msg)
}

func sendSMTPMessage(client *smtp.Client, cfg SMTPConfig, auth smtp.Auth, to, msg string) error {
	if auth != nil {
		if err := client.Auth(auth); err != nil {
			return fmt.Errorf("SMTP auth failed: %w", err)
		}
	}
	mailFrom := normalizeEnvelopeAddress(cfg.FromEmail)
	rcptTo := normalizeEnvelopeAddress(to)
	if err := client.Mail(mailFrom); err != nil {
		return fmt.Errorf("SMTP MAIL FROM failed: %w", err)
	}
	if err := client.Rcpt(rcptTo); err != nil {
		return fmt.Errorf("SMTP RCPT TO failed: %w", err)
	}
	w, err := client.Data()
	if err != nil {
		return fmt.Errorf("SMTP DATA failed: %w", err)
	}
	// codeql[go/email-injection]: buildMessage removes CR, LF, and NUL bytes from
	// request-derived content before it reaches this SMTP DATA sink, preventing
	// header and MIME-message injection.
	if _, err := w.Write([]byte(msg)); err != nil {
		return fmt.Errorf("SMTP write failed: %w", err)
	}
	if err := w.Close(); err != nil {
		return fmt.Errorf("SMTP close failed: %w", err)
	}
	// A successful DATA close means the server accepted the message. QUIT is
	// best-effort: reporting its failure would make the durable outbox resend an
	// already accepted delivery.
	_ = client.Quit()
	return nil
}

// normalizeEnvelopeAddress canonicalizes one SMTP envelope address. Unlike a
// MIME header, the envelope permits only the mailbox address, never a display
// name. The sanitized fallback keeps legacy integrations working.
func normalizeEnvelopeAddress(value string) string {
	value = sanitizeEmailContent(value)
	address, err := mail.ParseAddress(value)
	if err != nil {
		return value
	}
	return address.Address
}

func buildMessage(from, to, subject, htmlBody string) string {
	// SMTP headers end at the first blank line. Remove control characters from
	// dynamic content before composing the message so request or job data cannot
	// introduce a new header or alter the MIME body. Keep this at the message
	// construction boundary so every caller receives the same protection.
	from = normalizeMailboxHeader(from)
	to = normalizeRecipientHeader(to)
	subject = sanitizeEmailContent(subject)
	htmlBody = sanitizeEmailContent(htmlBody)

	var b strings.Builder
	b.WriteString(fmt.Sprintf("From: %s\r\n", encodeFromHeader(from)))
	b.WriteString(fmt.Sprintf("To: %s\r\n", to))
	b.WriteString(fmt.Sprintf("Subject: %s\r\n", mime.QEncoding.Encode("UTF-8", subject)))
	b.WriteString("MIME-Version: 1.0\r\n")
	b.WriteString("Content-Type: text/html; charset=UTF-8\r\n")
	b.WriteString("\r\n")
	b.WriteString(htmlBody)
	return b.String()
}

// normalizeMailboxHeader accepts one RFC 5322 mailbox and returns the
// canonical representation used in a message header. The sanitized fallback
// preserves compatibility with legacy configurations that net/mail rejects.
func normalizeMailboxHeader(value string) string {
	value = sanitizeEmailContent(value)
	address, err := mail.ParseAddress(value)
	if err != nil {
		return value
	}
	return address.String()
}

func normalizeRecipientHeader(value string) string {
	return normalizeMailboxHeader(value)
}

// encodeFromHeader RFC 2047-encodes the display name portion of a
// "Display Name <addr>" From header, leaving ASCII addresses untouched.
func encodeFromHeader(from string) string {
	if idx := strings.LastIndex(from, "<"); idx >= 0 {
		display := strings.TrimSpace(from[:idx])
		addr := strings.TrimSpace(from[idx:])
		if display == "" {
			return addr
		}
		return mime.QEncoding.Encode("UTF-8", display) + " " + addr
	}
	if strings.TrimSpace(from) == "" {
		return from
	}
	return mime.QEncoding.Encode("UTF-8", from)
}

// Localized mail builders keep the user-facing language choice out of HTTP
// handlers and worker code. German remains the legacy/default template.
func BuildPasswordResetEmailLocalized(url, language string) string {
	return buildActionEmail(language, i18n.T(language, "delivery.passwordReset.title"), i18n.T(language, "delivery.passwordReset.message"), i18n.T(language, "delivery.passwordReset.action"), url, i18n.T(language, "delivery.passwordReset.note"))
}

func BuildEmailChangeEmailLocalized(url, newEmail, language string) string {
	return buildActionEmail(language, i18n.T(language, "delivery.emailChange.title"), i18n.Format(language, "delivery.emailChange.message", map[string]string{"email": html.EscapeString(newEmail)}), i18n.T(language, "delivery.emailChange.action"), url, i18n.T(language, "delivery.emailChange.note"))
}

func BuildEmailChangedNotificationEmailLocalized(newEmail, language string) string {
	return buildActionEmail(language, i18n.T(language, "delivery.emailChanged.title"), i18n.Format(language, "delivery.emailChanged.message", map[string]string{"email": html.EscapeString(newEmail)}), "", "", "")
}

func BuildTestEmailLocalized(language string) string {
	return buildActionEmail(language, i18n.T(language, "delivery.smtpTest.title"), i18n.T(language, "delivery.smtpTest.message"), "", "", "")
}

// BuildNotificationEmailLocalized renders the delivery summary with the same
// restrained visual language as the application UI. Values are escaped before
// insertion because notification payloads may contain user-provided names.
func BuildNotificationEmailLocalized(kind, name, status, processed, total, failed, skipped, language string) string {
	rows := []struct {
		label string
		value string
	}{
		{i18n.T(language, "delivery.notification.status"), status},
		{i18n.T(language, "delivery.notification.processed"), processed + " / " + total},
		{i18n.T(language, "delivery.notification.failed"), failed},
		{i18n.T(language, "delivery.notification.skipped"), skipped},
	}

	var table strings.Builder
	for _, row := range rows {
		table.WriteString(fmt.Sprintf(`<tr><td class="summary-label" style="padding:10px 0;border-top:1px solid #e4e4e7;color:#71717a;font-size:13px;line-height:1.5">%s</td><td class="summary-value" style="padding:10px 0;border-top:1px solid #e4e4e7;color:#18181b;font-size:13px;font-weight:600;line-height:1.5;text-align:right;word-break:break-word;overflow-wrap:anywhere">%s</td></tr>`, html.EscapeString(row.label), html.EscapeString(row.value)))
	}

	title := fmt.Sprintf("%s · %s", i18n.T(language, "delivery.notification.kind."+kind), name)
	return buildEmailShell(language, title, fmt.Sprintf(`<p style="margin:0 0 20px;color:#52525b;font-size:14px;line-height:1.6;word-break:break-word;overflow-wrap:anywhere">%s</p><table role="presentation" width="100%%" cellspacing="0" cellpadding="0" border="0" style="width:100%%;border-collapse:collapse">%s</table>`, html.EscapeString(title), table.String()))
}

func PasswordResetSubject(language string) string {
	return i18n.T(language, "delivery.passwordReset.subject")
}
func EmailChangeSubject(language string) string {
	return i18n.T(language, "delivery.emailChange.subject")
}
func EmailChangedSubject(language string) string {
	return i18n.T(language, "delivery.emailChanged.subject")
}
func SMTPTestSubject(language string) string { return i18n.T(language, "delivery.smtpTest.subject") }

func buildActionEmail(language, title, message, action, actionURL, note string) string {
	button := ""
	if action != "" && actionURL != "" {
		button = fmt.Sprintf(`<table role="presentation" class="cta-table" width="100%%" cellspacing="0" cellpadding="0" border="0" style="width:100%%;margin:24px 0"><tr><td align="center"><a class="cta-link" href="%s" style="display:inline-block;padding:12px 20px;background:#18181b;border:1px solid #18181b;border-radius:4px;color:#ffffff;font-size:14px;font-weight:600;line-height:20px;text-align:center;text-decoration:none;word-break:break-word;overflow-wrap:anywhere">%s</a></td></tr></table>`, html.EscapeString(actionURL), html.EscapeString(action))
	}
	noteBlock := ""
	if note != "" {
		noteBlock = fmt.Sprintf(`<p style="margin:20px 0 0;color:#71717a;font-size:12px;line-height:1.6;word-break:break-word;overflow-wrap:anywhere">%s</p>`, html.EscapeString(note))
	}
	return buildEmailShell(language, title, fmt.Sprintf(`<p style="margin:0;color:#52525b;font-size:14px;line-height:1.6;word-break:break-word;overflow-wrap:anywhere">%s</p>%s%s`, message, button, noteBlock))
}

func buildEmailShell(language, title, content string) string {
	if language != "de" {
		language = "en"
	}
	return fmt.Sprintf(`<!DOCTYPE html>
<html lang="%s">
<head>
  <meta charset="UTF-8">
  <meta name="viewport" content="width=device-width, initial-scale=1.0">
  <style>
    @media screen and (max-width: 640px) {
      .email-outer { padding: 16px 12px !important; }
      .email-card { width: 100%% !important; }
      .email-header { padding: 18px 20px !important; }
      .email-body { padding: 24px 20px !important; }
      .email-footer { padding: 14px 20px !important; }
      .cta-table { margin: 20px 0 !important; }
      .cta-link { display: block !important; width: 100%% !important; box-sizing: border-box !important; }
      .summary-label, .summary-value { display: block !important; padding: 8px 0 !important; text-align: left !important; }
      .summary-value { border-top: 0 !important; padding-top: 0 !important; }
    }
  </style>
</head>
<body style="margin:0;padding:0;background:#fafafa;color:#18181b;font-family:Arial,Helvetica,sans-serif">
  <table role="presentation" width="100%%" cellspacing="0" cellpadding="0" border="0" style="width:100%%;background:#fafafa"><tr><td class="email-outer" style="padding:32px 16px">
    <table role="presentation" class="email-card" width="100%%" cellspacing="0" cellpadding="0" border="0" style="width:100%%;max-width:600px;margin:0 auto;background:#ffffff;border:1px solid #e4e4e7;border-radius:6px">
      <tr><td class="email-header" style="padding:20px 28px;border-bottom:1px solid #e4e4e7">
        <table role="presentation" cellspacing="0" cellpadding="0" border="0"><tr>
          <td style="padding:0 10px 0 0;vertical-align:middle"><img src="https://clumoove.com/clumoove_logo.svg" width="28" height="28" alt="Clumoove" style="display:block;border:0;outline:none;text-decoration:none" /></td>
          <td style="vertical-align:middle"><p style="margin:0;color:#18181b;font-size:18px;font-weight:700;letter-spacing:-0.3px;line-height:28px">Clumoove</p></td>
        </tr></table>
      </td></tr>
      <tr><td class="email-body" style="padding:28px"><h1 style="margin:0 0 16px;color:#18181b;font-size:20px;font-weight:700;line-height:1.3;word-break:break-word;overflow-wrap:anywhere">%s</h1>%s</td></tr>
      <tr><td class="email-footer" style="padding:16px 28px;border-top:1px solid #e4e4e7;color:#71717a;font-size:12px;line-height:1.5">Clumoove</td></tr>
    </table>
  </td></tr></table>
</body>
</html>`, language, html.EscapeString(title), content)
}
