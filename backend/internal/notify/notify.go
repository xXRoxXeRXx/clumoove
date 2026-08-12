// Package notify delivers completion snapshots without exposing channel secrets.
package notify

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/mail"
	"net/url"
	"strconv"
	"strings"
	"unicode/utf8"

	"backend/internal/email"
	"backend/internal/i18n"
	"backend/internal/storage"
)

type Config map[string]any

var (
	ErrIncomplete      = errors.New("notification config incomplete")
	ErrInvalidChannel  = errors.New("invalid notification channel")
	ErrInvalidURL      = errors.New("invalid notification URL")
	ErrInvalidPriority = errors.New("invalid ntfy priority")
	ErrInvalidPayload  = errors.New("invalid notification payload")
	ErrURLBlocked      = errors.New("notification URL blocked")
)

// newEgressHTTPClient is replaceable by package tests. Production delivery
// always uses storage's DNS-rebinding-safe egress client.
var newEgressHTTPClient = storage.NewEgressHTTPClient

type notificationPayload struct {
	Kind      string `json:"kind"`
	Name      string `json:"name"`
	Status    string `json:"status"`
	Processed int64  `json:"processed"`
	Total     int64  `json:"total"`
	Failed    int64  `json:"failed"`
	Skipped   int64  `json:"skipped"`
}

type validatedConfig struct {
	endpoint     string
	ntfyPriority string
}

func sanitizeSMTPValue(value string) string {
	return strings.NewReplacer("\r", "", "\n", "", "\x00", "").Replace(strings.TrimSpace(value))
}

func sanitizeSMTPAddressValue(value string) string {
	address, err := mail.ParseAddress(sanitizeSMTPValue(value))
	if err != nil {
		return ""
	}
	return address.Address
}

func smtpConfig(cfg Config) email.SMTPConfig {
	port := sanitizeSMTPValue(fmt.Sprint(cfg["smtp_port"]))
	if port == "" || port == "<nil>" || port == "0" {
		port = "587"
	}
	return email.SMTPConfig{
		Host:     sanitizeSMTPValue(fmt.Sprint(cfg["smtp_host"])),
		Port:     port,
		Username: sanitizeSMTPValue(fmt.Sprint(cfg["smtp_username"])),
		// Passwords are opaque authentication data. Trimming or removing
		// characters would change valid credentials, and they are never used
		// to construct SMTP commands or MIME headers.
		Password:   fmt.Sprint(cfg["smtp_password"]),
		FromEmail:  sanitizeSMTPAddressValue(fmt.Sprint(cfg["smtp_from_email"])),
		FromName:   sanitizeSMTPValue(fmt.Sprint(cfg["smtp_from_name"])),
		Encryption: sanitizeSMTPValue(fmt.Sprint(cfg["smtp_encryption"])),
	}
}

// Validate checks a user-supplied channel configuration before it is saved.
func Validate(typ string, cfg Config) error {
	validated, err := validateConfig(typ, cfg)
	if err != nil || validated.endpoint == "" {
		return err
	}
	if _, err := newEgressHTTPClient(validated.endpoint); err != nil {
		return fmt.Errorf("%w: %v", ErrURLBlocked, err)
	}
	return nil
}

func validateConfig(typ string, cfg Config) (validatedConfig, error) {
	required := func(keys ...string) bool {
		for _, key := range keys {
			value := fmt.Sprint(cfg[key])
			if strings.TrimSpace(value) == "" || value == "<nil>" {
				return false
			}
		}
		return true
	}
	validURL := func(key string) (string, error) {
		raw := strings.TrimSpace(fmt.Sprint(cfg[key]))
		parsed, err := url.Parse(raw)
		if err != nil || parsed.Hostname() == "" || (parsed.Scheme != "https" && parsed.Scheme != "http") {
			return "", ErrInvalidURL
		}
		// Plain HTTP remains supported for self-hosted gotify/ntfy instances on
		// trusted LANs. The egress client still blocks unsafe destinations.
		return raw, nil
	}

	switch typ {
	case "email":
		// Email is a preference-only channel. Its empty config is valid because
		// the worker loads and validates the instance mailer at send time.
		return validatedConfig{}, nil
	case "gotify":
		if !required("url", "token") {
			return validatedConfig{}, ErrIncomplete
		}
		endpoint, err := validURL("url")
		return validatedConfig{endpoint: endpoint}, err
	case "ntfy":
		if !required("url", "topic") {
			return validatedConfig{}, ErrIncomplete
		}
		priority, err := ntfyPriority(cfg)
		if err != nil {
			return validatedConfig{}, err
		}
		endpoint, err := validURL("url")
		return validatedConfig{endpoint: endpoint, ntfyPriority: priority}, err
	case "telegram":
		if !required("bot_token", "chat_id") {
			return validatedConfig{}, ErrIncomplete
		}
		return validatedConfig{}, nil
	case "discord":
		if !required("webhook_url") {
			return validatedConfig{}, ErrIncomplete
		}
		endpoint, err := validURL("webhook_url")
		return validatedConfig{endpoint: endpoint}, err
	default:
		return validatedConfig{}, ErrInvalidChannel
	}
}

func Send(ctx context.Context, typ string, cfg Config, rawPayload json.RawMessage, recipient, language string) error {
	validated, err := validateConfig(typ, cfg)
	if err != nil {
		return err
	}
	endpoint := validated.endpoint
	payload, err := decodePayload(rawPayload)
	if err != nil {
		return err
	}

	text := formatLocalized(payload, language)
	if typ == "discord" {
		text = truncate(text, 2000)
	}
	if typ == "telegram" {
		text = truncate(text, 4096)
	}
	if typ == "email" {
		smtpCfg := smtpConfig(cfg)
		return email.SendMailContext(ctx, smtpCfg, recipient, notificationSubject(language), email.BuildNotificationEmailLocalized(payload.Kind, payload.Name, payload.Status, strconv.FormatInt(payload.Processed, 10), strconv.FormatInt(payload.Total, 10), strconv.FormatInt(payload.Failed, 10), strconv.FormatInt(payload.Skipped, 10), language))
	}

	var body any
	headers := map[string]string{"Content-Type": "application/json"}
	switch typ {
	case "gotify":
		endpoint = strings.TrimRight(endpoint, "/") + "/message"
		body = map[string]any{"message": text, "title": notificationSubject(language)}
		headers["X-Gotify-Key"] = fmt.Sprint(cfg["token"])
	case "ntfy":
		endpoint = strings.TrimRight(endpoint, "/") + "/" + url.PathEscape(fmt.Sprint(cfg["topic"]))
		body = text
		headers["Content-Type"] = "text/plain; charset=utf-8"
		headers["Title"] = notificationSubject(language)
		if token := fmt.Sprint(cfg["token"]); token != "" && token != "<nil>" {
			headers["Authorization"] = "Bearer " + token
		}
		if validated.ntfyPriority != "" {
			headers["Priority"] = validated.ntfyPriority
		}
	case "telegram":
		endpoint = "https://api.telegram.org/bot" + url.PathEscape(fmt.Sprint(cfg["bot_token"])) + "/sendMessage"
		body = map[string]any{"chat_id": fmt.Sprint(cfg["chat_id"]), "text": text}
	case "discord":
		body = map[string]any{"content": text}
	}

	client, err := newEgressHTTPClient(endpoint)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrURLBlocked, err)
	}
	var requestBody []byte
	if typ == "ntfy" {
		requestBody = []byte(body.(string))
	} else {
		requestBody, err = json.Marshal(body)
		if err != nil {
			return err
		}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(requestBody))
	if err != nil {
		return err
	}
	for key, value := range headers {
		req.Header.Set(key, value)
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		_, _ = io.Copy(io.Discard, resp.Body)
		return fmt.Errorf("%s: remote status %d", typ, resp.StatusCode)
	}
	return nil
}

func ntfyPriority(cfg Config) (string, error) {
	value := strings.TrimSpace(fmt.Sprint(cfg["priority"]))
	if value == "" || value == "<nil>" {
		return "", nil
	}
	priority, err := strconv.Atoi(value)
	if err != nil || priority < 1 || priority > 5 {
		return "", ErrInvalidPriority
	}
	return strconv.Itoa(priority), nil
}

func decodePayload(raw json.RawMessage) (notificationPayload, error) {
	var payload notificationPayload
	if len(bytes.TrimSpace(raw)) == 0 || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return notificationPayload{}, ErrInvalidPayload
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		return notificationPayload{}, fmt.Errorf("%w: %v", ErrInvalidPayload, err)
	}
	if payload.Kind == "" || payload.Status == "" {
		return notificationPayload{}, ErrInvalidPayload
	}
	return payload, nil
}

func formatLocalized(payload notificationPayload, language string) string {
	return fmt.Sprintf("%s %s\n%s: %s\n%s: %d / %d\n%s: %d\n%s: %d", i18n.T(language, "delivery.notification.kind."+payload.Kind), payload.Name, i18n.T(language, "delivery.notification.status"), payload.Status, i18n.T(language, "delivery.notification.processed"), payload.Processed, payload.Total, i18n.T(language, "delivery.notification.failed"), payload.Failed, i18n.T(language, "delivery.notification.skipped"), payload.Skipped)
}

func notificationSubject(language string) string {
	return i18n.T(language, "delivery.notification.subject")
}

func truncate(value string, max int) string {
	if max < 3 || utf8.RuneCountInString(value) <= max {
		return value
	}
	runes := []rune(value)
	return string(runes[:max-3]) + "..."
}
