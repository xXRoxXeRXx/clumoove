// Package notify delivers completion snapshots without exposing channel secrets.
package notify

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"unicode/utf8"

	"backend/internal/email"
	"backend/internal/i18n"
	"backend/internal/storage"
)

type Config map[string]any

var ErrURLBlocked = errors.New("notification URL blocked")

func Validate(typ string, cfg Config) error {
	required := func(keys ...string) bool {
		for _, k := range keys {
			if strings.TrimSpace(fmt.Sprint(cfg[k])) == "" || fmt.Sprint(cfg[k]) == "<nil>" {
				return false
			}
		}
		return true
	}
	validURL := func(k string) error {
		raw := strings.TrimSpace(fmt.Sprint(cfg[k]))
		u, err := url.Parse(raw)
		if err != nil || u.Hostname() == "" || (u.Scheme != "https" && u.Scheme != "http") {
			return fmt.Errorf("invalid URL")
		}
		_, err = storage.NewEgressHTTPClient(raw)
		if err != nil {
			return fmt.Errorf("%w: %v", ErrURLBlocked, err)
		}
		return nil
	}
	switch typ {
	case "email":
		if !required("smtp_host", "smtp_username", "smtp_password", "smtp_from_email") {
			return fmt.Errorf("incomplete")
		}
		return email.ValidateSMTPHost(fmt.Sprint(cfg["smtp_host"]))
	case "gotify":
		if !required("url", "token") {
			return fmt.Errorf("incomplete")
		}
		return validURL("url")
	case "ntfy":
		if !required("url", "topic") {
			return fmt.Errorf("incomplete")
		}
		return validURL("url")
	case "telegram":
		if !required("bot_token", "chat_id") {
			return fmt.Errorf("incomplete")
		}
	case "discord":
		if !required("webhook_url") {
			return fmt.Errorf("incomplete")
		}
		return validURL("webhook_url")
	default:
		return fmt.Errorf("invalid channel")
	}
	return nil
}

func Send(ctx context.Context, typ string, cfg Config, payload json.RawMessage, recipient, language string) error {
	if err := Validate(typ, cfg); err != nil {
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
		port := fmt.Sprint(cfg["smtp_port"])
		if port == "" || port == "<nil>" {
			port = "587"
		}
		return email.SendMail(email.SMTPConfig{Host: fmt.Sprint(cfg["smtp_host"]), Port: port, Username: fmt.Sprint(cfg["smtp_username"]), Password: fmt.Sprint(cfg["smtp_password"]), FromEmail: fmt.Sprint(cfg["smtp_from_email"]), FromName: fmt.Sprint(cfg["smtp_from_name"]), Encryption: fmt.Sprint(cfg["smtp_encryption"])}, recipient, notificationSubject(language), "<pre>"+html.EscapeString(text)+"</pre>")
	}
	var endpoint string
	var body any
	headers := map[string]string{"Content-Type": "application/json"}
	switch typ {
	case "gotify":
		endpoint = strings.TrimRight(fmt.Sprint(cfg["url"]), "/") + "/message"
		body = map[string]any{"message": text, "title": notificationSubject(language)}
		headers["X-Gotify-Key"] = fmt.Sprint(cfg["token"])
	case "ntfy":
		endpoint = strings.TrimRight(fmt.Sprint(cfg["url"]), "/") + "/" + url.PathEscape(fmt.Sprint(cfg["topic"]))
		body = text
		headers["Content-Type"] = "text/plain; charset=utf-8"
		headers["Title"] = notificationSubject(language)
		if t := fmt.Sprint(cfg["token"]); t != "" && t != "<nil>" {
			headers["Authorization"] = "Bearer " + t
		}
		if p := fmt.Sprint(cfg["priority"]); p != "" && p != "<nil>" {
			headers["Priority"] = p
		}
	case "telegram":
		endpoint = "https://api.telegram.org/bot" + fmt.Sprint(cfg["bot_token"]) + "/sendMessage"
		body = map[string]any{"chat_id": fmt.Sprint(cfg["chat_id"]), "text": text}
	case "discord":
		endpoint = fmt.Sprint(cfg["webhook_url"])
		body = map[string]any{"content": text}
	}
	client, err := storage.NewEgressHTTPClient(endpoint)
	if err != nil {
		return err
	}
	var raw []byte
	if typ == "ntfy" {
		raw = []byte(body.(string))
	} else {
		raw, err = json.Marshal(body)
		if err != nil {
			return err
		}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(raw))
	if err != nil {
		return err
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("remote status %s", strconv.Itoa(resp.StatusCode))
	}
	return nil
}

func format(payload json.RawMessage) string {
	var p map[string]any
	_ = json.Unmarshal(payload, &p)
	return fmt.Sprintf("%s %s\nStatus: %v\nVerarbeitet: %v / %v\nFehler: %v\nÜbersprungen: %v", p["kind"], p["name"], p["status"], p["processed"], p["total"], p["failed"], p["skipped"])
}

func formatEnglish(payload json.RawMessage) string {
	var p map[string]any
	_ = json.Unmarshal(payload, &p)
	return fmt.Sprintf("%s %s\nStatus: %v\nProcessed: %v / %v\nFailed: %v\nSkipped: %v", p["kind"], p["name"], p["status"], p["processed"], p["total"], p["failed"], p["skipped"])
}

func formatLocalized(payload json.RawMessage, language string) string {
	var p map[string]any
	_ = json.Unmarshal(payload, &p)
	kind := fmt.Sprint(p["kind"])
	return fmt.Sprintf("%s %v\n%s: %v\n%s: %v / %v\n%s: %v\n%s: %v", i18n.T(language, "delivery.notification.kind."+kind), p["name"], i18n.T(language, "delivery.notification.status"), p["status"], i18n.T(language, "delivery.notification.processed"), p["processed"], p["total"], i18n.T(language, "delivery.notification.failed"), p["failed"], i18n.T(language, "delivery.notification.skipped"), p["skipped"])
}

func notificationSubject(language string) string {
	return i18n.T(language, "delivery.notification.subject")
}

func truncate(value string, max int) string {
	if utf8.RuneCountInString(value) <= max {
		return value
	}
	runes := []rune(value)
	return string(runes[:max-3]) + "..."
}
