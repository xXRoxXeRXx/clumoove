package email

import (
	"crypto/tls"
	"fmt"
	"html"
	"mime"
	"net"
	"net/smtp"
	"strings"

	"backend/internal/storage"
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


// ValidateSMTPHost checks that the SMTP host is not a private/internal IP
// address to prevent SSRF attacks. Literal internal IPs are rejected directly;
// hostnames are resolved and every returned address is inspected (mirroring
// storage.ValidateEgressHost), closing the DNS-rebinding case where a name
// points at an internal/metadata address such as 169.254.169.254.
func ValidateSMTPHost(host string) error {
	if host == "" {
		return fmt.Errorf("SMTP host is required")
	}

	if ip := net.ParseIP(host); ip != nil {
		if ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsUnspecified() {
			return fmt.Errorf("SMTP host must not be a private or internal IP address")
		}
		return nil
	}

	// Hostname: resolve and validate every address (SSRF / DNS-rebinding).
	if err := storage.ValidateEgressHost(host); err != nil {
		return fmt.Errorf("SMTP host must not resolve to a private or internal address")
	}
	return nil
}

func SendMail(cfg SMTPConfig, to, subject, htmlBody string) error {
	addr := net.JoinHostPort(cfg.Host, cfg.Port)

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
		return sendWithTLS(addr, cfg, auth, to, msg)
	case "starttls":
		return sendWithSTARTTLS(addr, cfg, auth, to, msg)
	default:
		return smtp.SendMail(addr, auth, cfg.FromEmail, []string{to}, []byte(msg))
	}
}

func sendWithTLS(addr string, cfg SMTPConfig, auth smtp.Auth, to, msg string) error {
	tlsConfig := &tls.Config{
		ServerName: cfg.Host,
	}

	conn, err := tls.Dial("tcp", addr, tlsConfig)
	if err != nil {
		return fmt.Errorf("TLS dial failed: %w", err)
	}
	defer conn.Close()

	client, err := smtp.NewClient(conn, cfg.Host)
	if err != nil {
		return fmt.Errorf("SMTP client creation failed: %w", err)
	}
	defer client.Close()

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
	_, err = w.Write([]byte(msg))
	if err != nil {
		return fmt.Errorf("SMTP write failed: %w", err)
	}
	if err := w.Close(); err != nil {
		return fmt.Errorf("SMTP close failed: %w", err)
	}

	return client.Quit()
}

func sendWithSTARTTLS(addr string, cfg SMTPConfig, auth smtp.Auth, to, msg string) error {
	conn, err := net.Dial("tcp", addr)
	if err != nil {
		return fmt.Errorf("dial failed: %w", err)
	}
	defer conn.Close()

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
	_, err = w.Write([]byte(msg))
	if err != nil {
		return fmt.Errorf("SMTP write failed: %w", err)
	}
	if err := w.Close(); err != nil {
		return fmt.Errorf("SMTP close failed: %w", err)
	}

	return client.Quit()
}

func buildMessage(from, to, subject, htmlBody string) string {
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

func BuildMigrationReportEmail(migrationID, status string, totalFiles, processedFiles, failedFiles, skippedFiles int, totalBytes, processedBytes int64, errorMessage string) string {
	statusColor := "#10b981"
	statusLabel := "Erfolgreich abgeschlossen"
	if status == "FAILED" {
		statusColor = "#ef4444"
		statusLabel = "Fehlgeschlagen"
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
			<h1 style="color:white;margin:0;font-size:24px;">Clumove</h1>
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
			<p style="margin:0;color:#9ca3af;font-size:11px;">Diese E-Mail wurde automatisch von Clumove generiert.</p>
		</div>
	</div>
</body>
</html>`, statusColor, statusLabel, migrationID, processedFiles, totalFiles, statusColor, failedFiles, skippedFiles, formatBytes(processedBytes), formatBytes(totalBytes), errorSection)
}


func BuildEmailChangeEmail(confirmURL, newEmail string) string {
	escapedURL := html.EscapeString(confirmURL)
	escapedEmail := html.EscapeString(newEmail)
	return fmt.Sprintf(`<!DOCTYPE html>
<html>
<body style="font-family:Arial,sans-serif;background:#f9fafb;padding:20px;">
	<div style="max-width:600px;margin:0 auto;background:white;border-radius:12px;overflow:hidden;box-shadow:0 1px 3px rgba(0,0,0,0.1);">
		<div style="background:linear-gradient(135deg,#f97316,#ea580c);padding:24px;text-align:center;">
			<h1 style="color:white;margin:0;font-size:24px;">Clumove</h1>
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
			<p style="margin:0;color:#9ca3af;font-size:11px;">Diese E-Mail wurde automatisch von Clumove generiert.</p>
		</div>
	</div>
</body>
</html>`, escapedEmail, escapedURL, escapedURL)
}

func BuildEmailChangedNotificationEmail(newEmail string) string {
	escapedEmail := html.EscapeString(newEmail)
	return fmt.Sprintf(`<!DOCTYPE html>
<html>
<body style="font-family:Arial,sans-serif;background:#f9fafb;padding:20px;">
	<div style="max-width:600px;margin:0 auto;background:white;border-radius:12px;overflow:hidden;box-shadow:0 1px 3px rgba(0,0,0,0.1);">
		<div style="background:linear-gradient(135deg,#f97316,#ea580c);padding:24px;text-align:center;">
			<h1 style="color:white;margin:0;font-size:24px;">Clumove</h1>
			<p style="color:rgba(255,255,255,0.9);margin:8px 0 0;font-size:14px;">E-Mail-Adresse geändert</p>
		</div>
		<div style="padding:30px;text-align:center;">
			<div style="display:inline-block;padding:16px;background:#ecfdf5;border-radius:50%%;margin-bottom:20px;">
				<span style="font-size:32px;">&#10003;</span>
			</div>
			<h2 style="color:#065f46;margin:0 0 10px;">Änderung erfolgreich!</h2>
			<p style="color:#6b7280;font-size:14px;line-height:1.6;">
				Deine Clumove-Konto-E-Mail-Adresse ist nun <strong>%s</strong>. Du wirst bei künftigen Anmeldungen diese Adresse verwenden müssen.
			</p>
		</div>
		<div style="background:#f9fafb;padding:16px;text-align:center;border-top:1px solid #f3f4f6;">
			<p style="margin:0;color:#9ca3af;font-size:11px;">Diese E-Mail wurde automatisch von Clumove generiert.</p>
		</div>
	</div>
</body>
</html>`, escapedEmail)
}

func BuildPasswordResetEmail(resetURL string) string {
	escapedURL := html.EscapeString(resetURL)
	return fmt.Sprintf(`<!DOCTYPE html>
<html>
<body style="font-family:Arial,sans-serif;background:#f9fafb;padding:20px;">
	<div style="max-width:600px;margin:0 auto;background:white;border-radius:12px;overflow:hidden;box-shadow:0 1px 3px rgba(0,0,0,0.1);">
		<div style="background:linear-gradient(135deg,#f97316,#ea580c);padding:24px;text-align:center;">
			<h1 style="color:white;margin:0;font-size:24px;">Clumove</h1>
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
			<p style="margin:0;color:#9ca3af;font-size:11px;">Diese E-Mail wurde automatisch von Clumove generiert.</p>
		</div>
	</div>
</body>
</html>`, escapedURL, escapedURL)
}

func BuildTestEmail() string {
	return `<!DOCTYPE html>
<html>
<body style="font-family:Arial,sans-serif;background:#f9fafb;padding:20px;">
	<div style="max-width:600px;margin:0 auto;background:white;border-radius:12px;overflow:hidden;box-shadow:0 1px 3px rgba(0,0,0,0.1);">
		<div style="background:linear-gradient(135deg,#f97316,#ea580c);padding:24px;text-align:center;">
			<h1 style="color:white;margin:0;font-size:24px;">Clumove</h1>
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
			<p style="margin:0;color:#9ca3af;font-size:11px;">Diese E-Mail wurde automatisch von Clumove generiert.</p>
		</div>
	</div>
</body>
</html>`
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
