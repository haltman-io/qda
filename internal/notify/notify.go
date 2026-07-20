// Package notify delivers scan notifications through Discord, Telegram,
// Slack, a generic webhook, SMTP email and GitHub issues.
package notify

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/smtp"
	"net/url"
	"strings"
	"time"

	"qda/internal/config"
	"qda/internal/output"
	"qda/internal/types"
)

// Payload is the notification content.
type Payload struct {
	Title     string   `json:"title"`
	Message   string   `json:"message"`
	Total     int      `json:"total"`
	Available int      `json:"available"`
	Premium   int      `json:"premium"`
	Soon      int      `json:"soon"`
	Registered int     `json:"registered"`
	Duration  string   `json:"duration,omitempty"`
	Domains   []string `json:"domains,omitempty"`
}

// FinishPayload builds the end-of-scan payload.
func FinishPayload(results []types.Result, elapsed time.Duration) Payload {
	counts := output.CountResults(results)
	payload := Payload{
		Title:      "qda scan finished",
		Total:      len(results),
		Available:  counts[types.AvailabilityAvailable],
		Premium:    counts[types.AvailabilityPremium],
		Registered: counts[types.AvailabilityRegistered],
		Duration:   elapsed.Round(time.Second).String(),
	}
	var domains []string
	for _, result := range results {
		if types.IsAvailableSoon(result) && !types.IsAvailableLike(result) {
			payload.Soon++
		}
		if types.IsAvailableLike(result) || types.IsAvailableSoon(result) {
			if len(domains) < 25 {
				domains = append(domains, result.Domain)
			}
		}
	}
	payload.Domains = domains

	var b strings.Builder
	fmt.Fprintf(&b, "qda scan finished in %s: %d checked, %d available, %d premium, %d soon, %d registered.",
		payload.Duration, payload.Total, payload.Available, payload.Premium, payload.Soon, payload.Registered)
	if len(domains) > 0 {
		fmt.Fprintf(&b, "\n\nInteresting domains:\n- %s", strings.Join(domains, "\n- "))
	}
	payload.Message = b.String()
	return payload
}

// AvailablePayload builds a per-domain notification payload.
func AvailablePayload(result types.Result) Payload {
	return Payload{
		Title:   "qda: " + string(result.Availability) + " " + result.Domain,
		Message: fmt.Sprintf("%s is %s (source: %s)", result.Domain, output.StatusLabel(result), result.Source),
		Domains: []string{result.Domain},
	}
}

// Send dispatches the payload to every configured channel. Errors are
// aggregated so one failing channel does not block the others.
func Send(ctx context.Context, settings config.Settings, payload Payload) error {
	var errs []string
	notify := settings.Notify

	if notify.DiscordWebhook != "" {
		if err := sendWebhookJSON(ctx, settings, notify.DiscordWebhook, map[string]string{"content": payload.Message}); err != nil {
			errs = append(errs, "discord: "+err.Error())
		}
	}
	if notify.SlackWebhook != "" {
		if err := sendWebhookJSON(ctx, settings, notify.SlackWebhook, map[string]string{"text": payload.Message}); err != nil {
			errs = append(errs, "slack: "+err.Error())
		}
	}
	if notify.WebhookURL != "" {
		if err := sendWebhookJSON(ctx, settings, notify.WebhookURL, payload); err != nil {
			errs = append(errs, "webhook: "+err.Error())
		}
	}
	if notify.TelegramToken != "" || notify.TelegramChatID != "" {
		if notify.TelegramToken == "" || notify.TelegramChatID == "" {
			errs = append(errs, "telegram: bot_token and chat_id must be set together")
		} else if err := sendTelegram(ctx, settings, payload); err != nil {
			errs = append(errs, "telegram: "+err.Error())
		}
	}
	if notify.Email.Host != "" {
		if err := sendEmail(settings, payload); err != nil {
			errs = append(errs, "email: "+err.Error())
		}
	}
	if notify.GitHub.Token != "" && notify.GitHub.Owner != "" && notify.GitHub.Repo != "" {
		if err := sendGitHubIssue(ctx, settings, payload); err != nil {
			errs = append(errs, "github: "+err.Error())
		}
	}

	if len(errs) > 0 {
		return fmt.Errorf("notification errors: %s", strings.Join(errs, "; "))
	}
	return nil
}

// HasChannels reports whether at least one channel is configured.
func HasChannels(settings config.Settings) bool {
	n := settings.Notify
	return n.DiscordWebhook != "" || n.SlackWebhook != "" || n.WebhookURL != "" ||
		(n.TelegramToken != "" && n.TelegramChatID != "") || n.Email.Host != "" ||
		(n.GitHub.Token != "" && n.GitHub.Owner != "" && n.GitHub.Repo != "")
}

func sendWebhookJSON(ctx context.Context, settings config.Settings, webhookURL string, body any) error {
	data, err := json.Marshal(body)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, webhookURL, bytes.NewReader(data))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", settings.UserAgent)

	client := &http.Client{Timeout: settings.Timeout}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	return nil
}

func sendTelegram(ctx context.Context, settings config.Settings, payload Payload) error {
	form := url.Values{}
	form.Set("chat_id", settings.Notify.TelegramChatID)
	form.Set("text", payload.Message)
	form.Set("disable_web_page_preview", "true")

	base := strings.TrimRight(settings.Notify.TelegramAPIBase, "/")
	if base == "" {
		base = "https://api.telegram.org"
	}
	endpoint := base + "/bot" + url.PathEscape(settings.Notify.TelegramToken) + "/sendMessage"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("User-Agent", settings.UserAgent)

	client := &http.Client{Timeout: settings.Timeout}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	return nil
}

func sendEmail(settings config.Settings, payload Payload) error {
	email := settings.Notify.Email
	if email.From == "" || len(email.To) == 0 {
		return fmt.Errorf("from and to are required")
	}
	port := email.Port
	if port == 0 {
		port = 587
	}
	addr := net.JoinHostPort(email.Host, fmt.Sprint(port))

	var b strings.Builder
	fmt.Fprintf(&b, "From: %s\r\n", email.From)
	fmt.Fprintf(&b, "To: %s\r\n", strings.Join(email.To, ", "))
	fmt.Fprintf(&b, "Subject: %s\r\n", payload.Title)
	fmt.Fprintf(&b, "Content-Type: text/plain; charset=utf-8\r\n\r\n")
	b.WriteString(payload.Message)
	b.WriteString("\r\n")

	message := []byte(b.String())
	auth := smtp.PlainAuth("", email.Username, email.Password, email.Host)
	if email.Username == "" {
		auth = nil
	}

	switch strings.ToLower(email.TLSMode) {
	case "tls", "smtps":
		conn, err := tls.Dial("tcp", addr, &tls.Config{ServerName: email.Host})
		if err != nil {
			return err
		}
		client, err := smtp.NewClient(conn, email.Host)
		if err != nil {
			return err
		}
		defer client.Close()
		if auth != nil {
			if err := client.Auth(auth); err != nil {
				return err
			}
		}
		if err := client.Mail(email.From); err != nil {
			return err
		}
		for _, to := range email.To {
			if err := client.Rcpt(to); err != nil {
				return err
			}
		}
		writer, err := client.Data()
		if err != nil {
			return err
		}
		if _, err := writer.Write(message); err != nil {
			return err
		}
		if err := writer.Close(); err != nil {
			return err
		}
		return client.Quit()
	case "none", "plain", "":
		return smtp.SendMail(addr, auth, email.From, email.To, message)
	default: // starttls
		return smtp.SendMail(addr, auth, email.From, email.To, message)
	}
}

func sendGitHubIssue(ctx context.Context, settings config.Settings, payload Payload) error {
	github := settings.Notify.GitHub
	base := strings.TrimRight(github.APIBase, "/")
	if base == "" {
		base = "https://api.github.com"
	}
	endpoint := fmt.Sprintf("%s/repos/%s/%s/issues", base, github.Owner, github.Repo)

	body, err := json.Marshal(map[string]any{
		"title":  payload.Title,
		"body":   payload.Message,
		"labels": []string{"qda"},
	})
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+github.Token)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", settings.UserAgent)

	client := &http.Client{Timeout: settings.Timeout}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	return nil
}
