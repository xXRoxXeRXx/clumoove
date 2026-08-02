package email

import (
	"context"
	"crypto/tls"
	"fmt"
	"html"
	"mime"
	"net"
	"net/smtp"
	"sort"
	"strings"
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
	Encryption string // tls, starttls, none
}

const smtpOperationTimeout = 15 * time.Second

var resolveSMTPIPs = func(ctx context.Context, host string) ([]net.IP, error) {
	return net.DefaultResolver.LookupIP(ctx, "ip", host)
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

	ips, err := resolveSMTPIPs(ctx, host)
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
	return ip.IsGlobalUnicast() && !ip.IsPrivate()
}

func SendMail(cfg SMTPConfig, to, subject, htmlBody string) error {
	ctx, cancel := context.WithTimeout(context.Background(), smtpOperationTimeout)
	defer cancel()

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

	switch strings.ToLower(cfg.Encryption) {
	case "tls":
		return sendWithTLS(ctx, cfg, auth, to, msg)
	case "starttls":
		return sendWithSTARTTLS(ctx, cfg, auth, to, msg)
	default:
		return sendWithoutTLS(ctx, cfg, auth, to, msg)
	}
}

// sanitizeEmailContent removes characters that have special meaning in an SMTP
// message. HTML templates do not require line breaks, so stripping them is safe
// and prevents CRLF-based message and header injection.
func sanitizeEmailContent(value string) string {
	return strings.NewReplacer("\r", "", "\n", "", "\x00", "").Replace(value)
}

func smtpDialContext(ctx context.Context, host, port string) (net.Conn, error) {
	ips, err := resolveSMTPIPs(ctx, host)
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
	}
	if err := client.StartTLS(tlsConfig); err != nil {
		return fmt.Errorf("STARTTLS failed: %w", err)
	}

	return sendSMTPMessage(client, cfg, auth, to, msg)
}

func sendWithoutTLS(ctx context.Context, cfg SMTPConfig, auth smtp.Auth, to, msg string) error {
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

	return sendSMTPMessage(client, cfg, auth, to, msg)
}

func sendSMTPMessage(client *smtp.Client, cfg SMTPConfig, auth smtp.Auth, to, msg string) error {
	if auth != nil {
		if err := client.Auth(auth); err != nil {
			return fmt.Errorf("SMTP auth failed: %w", err)
		}
	}
	if err := client.Mail(cfg.FromEmail); err != nil {
		return fmt.Errorf("SMTP MAIL FROM failed: %w", err)
	}
	if err := client.Rcpt(to); err != nil {
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
	return client.Quit()
}

func buildMessage(from, to, subject, htmlBody string) string {
	// SMTP headers end at the first blank line. Remove control characters from
	// dynamic content before composing the message so request or job data cannot
	// introduce a new header or alter the MIME body. Keep this at the message
	// construction boundary so every caller receives the same protection.
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

// BuildMigrationReportEmail is retained for source compatibility only. The
// notifier uses the localized delivery catalog and does not call this legacy
// German-only report builder.
func BuildMigrationReportEmail(migrationID, status string, totalFiles, processedFiles, failedFiles, skippedFiles int, totalBytes, processedBytes int64, errorMessage string) string {
	statusColor := "#10b981"
	statusLabel := "Erfolgreich abgeschlossen"
	if status == "FAILED" {
		statusColor = "#ef4444"
		statusLabel = "Fehlgeschlagen"
	} else if status == "COMPLETED_WITH_ERRORS" {
		statusColor = "#f59e0b"
		statusLabel = "Abgeschlossen mit Fehlern"
	}

	errorSection := ""
	if errorMessage != "" {
		errorSection = fmt.Sprintf(`
			<div style="margin-top:20px;padding:15px;background:#fef2f2;border:1px solid #fecaca;border-radius:8px;">
				<strong style="color:#991b1b;">Fehlermeldung:</strong>
				<p style="color:#991b1b;margin:5px 0 0;">%s</p>
			</div>`, html.EscapeString(errorMessage))
	}

	return fmt.Sprintf(`<!DOCTYPE html>
<html>
<body style="font-family:Arial,sans-serif;background:#f9fafb;padding:20px;">
	<div style="max-width:600px;margin:0 auto;background:white;border-radius:12px;overflow:hidden;box-shadow:0 1px 3px rgba(0,0,0,0.1);">
		<div style="background:linear-gradient(135deg,#f97316,#ea580c);padding:24px;text-align:center;">
			<h1 style="color:white;margin:0;font-size:24px;">Clumoove</h1>
			<p style="color:rgba(255,255,255,0.9);margin:8px 0 0;font-size:14px;">Migrationsbericht</p>
		</div>
		<div style="padding:30px;">
			<div style="text-align:center;margin-bottom:24px;">
				<span style="display:inline-block;padding:8px 20px;background:%s;color:white;border-radius:20px;font-weight:bold;font-size:14px;">%s</span>
			</div>
			<table style="width:100%%;border-collapse:collapse;margin-bottom:20px;">
				<tr><td style="padding:8px 0;color:#6b7280;font-size:13px;">Migration ID</td><td style="padding:8px 0;text-align:right;font-family:monospace;font-size:13px;">%s</td></tr>
				<tr><td style="padding:8px 0;color:#6b7280;font-size:13px;border-top:1px solid #f3f4f6;">Dateien verarbeitet</td><td style="padding:8px 0;text-align:right;font-size:13px;border-top:1px solid #f3f4f6;">%d / %d</td></tr>
				<tr><td style="padding:8px 0;color:#6b7280;font-size:13px;border-top:1px solid #f3f4f6;">Fehlgeschlagen</td><td style="padding:8px 0;text-align:right;font-size:13px;border-top:1px solid #f3f4f6;color:%s;">%d</td></tr>
				<tr><td style="padding:8px 0;color:#6b7280;font-size:13px;border-top:1px solid #f3f4f6;">Übersprungen</td><td style="padding:8px 0;text-align:right;font-size:13px;border-top:1px solid #f3f4f6;">%d</td></tr>
				<tr><td style="padding:8px 0;color:#6b7280;font-size:13px;border-top:1px solid #f3f4f6;">Daten übertragen</td><td style="padding:8px 0;text-align:right;font-size:13px;border-top:1px solid #f3f4f6;">%s / %s</td></tr>
			</table>
			%s
		</div>
		<div style="background:#f9fafb;padding:16px;text-align:center;border-top:1px solid #f3f4f6;">
			<p style="margin:0;color:#9ca3af;font-size:11px;">Diese E-Mail wurde automatisch von Clumoove generiert.</p>
		</div>
	</div>
</body>
</html>`, statusColor, statusLabel, migrationID, processedFiles, totalFiles, statusColor, failedFiles, skippedFiles, formatBytes(processedBytes), formatBytes(totalBytes), errorSection)
}

// BuildEmailChangeEmail is a legacy German template. New delivery uses
// BuildEmailChangeEmailLocalized and the delivery.* catalog.
func BuildEmailChangeEmail(confirmURL, newEmail string) string {
	escapedURL := html.EscapeString(confirmURL)
	escapedEmail := html.EscapeString(newEmail)
	return fmt.Sprintf(`<!DOCTYPE html>
<html>
<body style="font-family:Arial,sans-serif;background:#f9fafb;padding:20px;">
	<div style="max-width:600px;margin:0 auto;background:white;border-radius:12px;overflow:hidden;box-shadow:0 1px 3px rgba(0,0,0,0.1);">
		<div style="background:linear-gradient(135deg,#f97316,#ea580c);padding:24px;text-align:center;">
			<h1 style="color:white;margin:0;font-size:24px;">Clumoove</h1>
			<p style="color:rgba(255,255,255,0.9);margin:8px 0 0;font-size:14px;">E-Mail-Adresse ändern</p>
		</div>
		<div style="padding:30px;">
			<p style="color:#374151;font-size:14px;line-height:1.6;">
				Du hast eine Änderung deiner E-Mail-Adresse auf <strong>%s</strong> angefordert. Bestätige die Änderung, indem du auf den Button unten klickst.
			</p>
			<div style="text-align:center;margin:30px 0;">
				<a href="%s" style="display:inline-block;padding:14px 32px;background:linear-gradient(135deg,#f97316,#ea580c);color:white;text-decoration:none;border-radius:10px;font-weight:bold;font-size:14px;">
					E-Mail-Adresse bestätigen
				</a>
			</div>
			<p style="color:#6b7280;font-size:12px;line-height:1.6;">
				Der Link ist 4 Stunden gültig. Falls du diese Änderung nicht angefordert hast, kannst du diese E-Mail ignorieren. Deine E-Mail-Adresse bleibt unverändert.
			</p>
			<div style="margin-top:20px;padding:12px;background:#f9fafb;border-radius:8px;word-break:break-all;">
				<p style="margin:0;color:#9ca3af;font-size:11px;">Falls der Button nicht funktioniert, kopiere diesen Link in deinen Browser:</p>
				<p style="margin:5px 0 0;color:#6b7280;font-size:11px;font-family:monospace;">%s</p>
			</div>
		</div>
		<div style="background:#f9fafb;padding:16px;text-align:center;border-top:1px solid #f3f4f6;">
			<p style="margin:0;color:#9ca3af;font-size:11px;">Diese E-Mail wurde automatisch von Clumoove generiert.</p>
		</div>
	</div>
</body>
</html>`, escapedEmail, escapedURL, escapedURL)
}

// BuildEmailChangedNotificationEmail is a legacy German template. New delivery
// uses BuildEmailChangedNotificationEmailLocalized and the delivery.* catalog.
func BuildEmailChangedNotificationEmail(newEmail string) string {
	escapedEmail := html.EscapeString(newEmail)
	return fmt.Sprintf(`<!DOCTYPE html>
<html>
<body style="font-family:Arial,sans-serif;background:#f9fafb;padding:20px;">
	<div style="max-width:600px;margin:0 auto;background:white;border-radius:12px;overflow:hidden;box-shadow:0 1px 3px rgba(0,0,0,0.1);">
		<div style="background:linear-gradient(135deg,#f97316,#ea580c);padding:24px;text-align:center;">
			<h1 style="color:white;margin:0;font-size:24px;">Clumoove</h1>
			<p style="color:rgba(255,255,255,0.9);margin:8px 0 0;font-size:14px;">E-Mail-Adresse geändert</p>
		</div>
		<div style="padding:30px;text-align:center;">
			<div style="display:inline-block;padding:16px;background:#ecfdf5;border-radius:50%%;margin-bottom:20px;">
				<span style="font-size:32px;">&#10003;</span>
			</div>
			<h2 style="color:#065f46;margin:0 0 10px;">Änderung erfolgreich!</h2>
			<p style="color:#6b7280;font-size:14px;line-height:1.6;">
				Deine Clumoove-Konto-E-Mail-Adresse ist nun <strong>%s</strong>. Du wirst bei künftigen Anmeldungen diese Adresse verwenden müssen.
			</p>
		</div>
		<div style="background:#f9fafb;padding:16px;text-align:center;border-top:1px solid #f3f4f6;">
			<p style="margin:0;color:#9ca3af;font-size:11px;">Diese E-Mail wurde automatisch von Clumoove generiert.</p>
		</div>
	</div>
</body>
</html>`, escapedEmail)
}

// BuildPasswordResetEmail is a legacy German template. New delivery uses
// BuildPasswordResetEmailLocalized and the delivery.* catalog.
func BuildPasswordResetEmail(resetURL string) string {
	escapedURL := html.EscapeString(resetURL)
	return fmt.Sprintf(`<!DOCTYPE html>
<html>
<body style="font-family:Arial,sans-serif;background:#f9fafb;padding:20px;">
	<div style="max-width:600px;margin:0 auto;background:white;border-radius:12px;overflow:hidden;box-shadow:0 1px 3px rgba(0,0,0,0.1);">
		<div style="background:linear-gradient(135deg,#f97316,#ea580c);padding:24px;text-align:center;">
			<h1 style="color:white;margin:0;font-size:24px;">Clumoove</h1>
			<p style="color:rgba(255,255,255,0.9);margin:8px 0 0;font-size:14px;">Passwort zurücksetzen</p>
		</div>
		<div style="padding:30px;">
			<p style="color:#374151;font-size:14px;line-height:1.6;">
				Du hast eine Anfrage zum Zurücksetzen deines Passworts erhalten. Klicke auf den Button unten, um ein neues Passwort festzulegen.
			</p>
			<div style="text-align:center;margin:30px 0;">
				<a href="%s" style="display:inline-block;padding:14px 32px;background:linear-gradient(135deg,#f97316,#ea580c);color:white;text-decoration:none;border-radius:10px;font-weight:bold;font-size:14px;">
					Passwort zurücksetzen
				</a>
			</div>
			<p style="color:#6b7280;font-size:12px;line-height:1.6;">
				Der Link ist 4 Stunden gültig. Falls du diese E-Mail nicht angefordert hast, kannst du sie ignorieren. Dein Passwort bleibt unverändert.
			</p>
			<div style="margin-top:20px;padding:12px;background:#f9fafb;border-radius:8px;word-break:break-all;">
				<p style="margin:0;color:#9ca3af;font-size:11px;">Falls der Button nicht funktioniert, kopiere diesen Link in deinen Browser:</p>
				<p style="margin:5px 0 0;color:#6b7280;font-size:11px;font-family:monospace;">%s</p>
			</div>
		</div>
		<div style="background:#f9fafb;padding:16px;text-align:center;border-top:1px solid #f3f4f6;">
			<p style="margin:0;color:#9ca3af;font-size:11px;">Diese E-Mail wurde automatisch von Clumoove generiert.</p>
		</div>
	</div>
</body>
</html>`, escapedURL, escapedURL)
}

// BuildTestEmail is a legacy German template. New delivery uses
// BuildTestEmailLocalized and the delivery.* catalog.
func BuildTestEmail() string {
	return `<!DOCTYPE html>
<html>
<body style="font-family:Arial,sans-serif;background:#f9fafb;padding:20px;">
	<div style="max-width:600px;margin:0 auto;background:white;border-radius:12px;overflow:hidden;box-shadow:0 1px 3px rgba(0,0,0,0.1);">
		<div style="background:linear-gradient(135deg,#f97316,#ea580c);padding:24px;text-align:center;">
			<h1 style="color:white;margin:0;font-size:24px;">Clumoove</h1>
			<p style="color:rgba(255,255,255,0.9);margin:8px 0 0;font-size:14px;">SMTP-Test</p>
		</div>
		<div style="padding:30px;text-align:center;">
			<div style="display:inline-block;padding:16px;background:#ecfdf5;border-radius:50%;margin-bottom:20px;">
				<span style="font-size:32px;">&#10003;</span>
			</div>
			<h2 style="color:#065f46;margin:0 0 10px;">SMTP-Verbindung erfolgreich!</h2>
			<p style="color:#6b7280;font-size:14px;">Deine SMTP-Einstellungen sind korrekt konfiguriert. Du wirst bei Abschluss von Migrationen per E-Mail benachrichtigt.</p>
		</div>
		<div style="background:#f9fafb;padding:16px;text-align:center;border-top:1px solid #f3f4f6;">
			<p style="margin:0;color:#9ca3af;font-size:11px;">Diese E-Mail wurde automatisch von Clumoove generiert.</p>
		</div>
	</div>
</body>
</html>`
}

// Localized mail builders keep the user-facing language choice out of HTTP
// handlers and worker code. German remains the legacy/default template.
func BuildPasswordResetEmailLocalized(url, language string) string {
	return buildActionEmail(i18n.T(language, "delivery.passwordReset.title"), i18n.T(language, "delivery.passwordReset.message"), i18n.T(language, "delivery.passwordReset.action"), url, i18n.T(language, "delivery.passwordReset.note"))
}

func BuildEmailChangeEmailLocalized(url, newEmail, language string) string {
	return buildActionEmail(i18n.T(language, "delivery.emailChange.title"), i18n.Format(language, "delivery.emailChange.message", map[string]string{"email": html.EscapeString(newEmail)}), i18n.T(language, "delivery.emailChange.action"), url, i18n.T(language, "delivery.emailChange.note"))
}

func BuildEmailChangedNotificationEmailLocalized(newEmail, language string) string {
	return buildActionEmail(i18n.T(language, "delivery.emailChanged.title"), i18n.Format(language, "delivery.emailChanged.message", map[string]string{"email": html.EscapeString(newEmail)}), "", "", "")
}

func BuildTestEmailLocalized(language string) string {
	return buildActionEmail(i18n.T(language, "delivery.smtpTest.title"), i18n.T(language, "delivery.smtpTest.message"), "", "", "")
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
		table.WriteString(fmt.Sprintf(`<tr><td style="padding:10px 0;border-top:1px solid #e4e4e7;color:#71717a;font-size:13px">%s</td><td style="padding:10px 0;border-top:1px solid #e4e4e7;color:#18181b;font-size:13px;font-weight:600;text-align:right">%s</td></tr>`, html.EscapeString(row.label), html.EscapeString(row.value)))
	}

	title := fmt.Sprintf("%s · %s", i18n.T(language, "delivery.notification.kind."+kind), name)
	return buildEmailShell(title, fmt.Sprintf(`<p style="margin:0 0 20px;color:#52525b;font-size:14px;line-height:1.6">%s</p><table role="presentation" style="width:100%%;border-collapse:collapse">%s</table>`, html.EscapeString(title), table.String()))
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

func buildActionEmail(title, message, action, actionURL, note string) string {
	button := ""
	if action != "" && actionURL != "" {
		button = fmt.Sprintf(`<p style="margin:24px 0;text-align:center"><a href="%s" style="display:inline-block;padding:12px 20px;background:#18181b;border:1px solid #18181b;border-radius:4px;color:#ffffff;font-size:14px;font-weight:600;line-height:20px;text-decoration:none">%s</a></p>`, html.EscapeString(actionURL), html.EscapeString(action))
	}
	noteBlock := ""
	if note != "" {
		noteBlock = fmt.Sprintf(`<p style="margin:20px 0 0;color:#71717a;font-size:12px;line-height:1.6">%s</p>`, html.EscapeString(note))
	}
	return buildEmailShell(title, fmt.Sprintf(`<p style="margin:0;color:#52525b;font-size:14px;line-height:1.6">%s</p>%s%s`, message, button, noteBlock))
}

func buildEmailShell(title, content string) string {
	return fmt.Sprintf(`<!DOCTYPE html>
<html lang="en">
<head><meta charset="UTF-8"><meta name="viewport" content="width=device-width, initial-scale=1.0"></head>
<body style="margin:0;padding:0;background:#fafafa;color:#18181b;font-family:Arial,Helvetica,sans-serif">
  <table role="presentation" width="100%%" cellspacing="0" cellpadding="0" border="0" style="width:100%%;background:#fafafa"><tr><td style="padding:32px 16px">
    <table role="presentation" width="100%%" cellspacing="0" cellpadding="0" border="0" style="max-width:600px;margin:0 auto;background:#ffffff;border:1px solid #e4e4e7;border-radius:6px">
      <tr><td style="padding:24px 28px;border-bottom:1px solid #e4e4e7"><p style="margin:0;color:#18181b;font-size:20px;font-weight:700;letter-spacing:-0.3px">Clumoove</p></td></tr>
      <tr><td style="padding:28px"><h1 style="margin:0 0 16px;color:#18181b;font-size:20px;font-weight:700;line-height:1.3">%s</h1>%s</td></tr>
      <tr><td style="padding:16px 28px;border-top:1px solid #e4e4e7;color:#71717a;font-size:12px;line-height:1.5">Clumoove</td></tr>
    </table>
  </td></tr></table>
</body>
</html>`, html.EscapeString(title), content)
}

func formatBytes(b int64) string {
	const unit = 1024
	if b < unit {
		return fmt.Sprintf("%d B", b)
	}
	div, exp := int64(unit), 0
	for n := b / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(b)/float64(div), "KMGTPE"[exp])
}
