package qda

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
)

func SendNotifications(ctx context.Context, settings Settings, results []Result) error {
	if settings.DiscordWebhookURL != "" {
		if err := sendDiscord(ctx, settings, results); err != nil {
			return err
		}
	}
	if settings.TelegramBotToken != "" || settings.TelegramChatID != "" {
		if settings.TelegramBotToken == "" || settings.TelegramChatID == "" {
			return fmt.Errorf("telegram-token and telegram-chat must be set together")
		}
		if err := sendTelegram(ctx, settings, results); err != nil {
			return err
		}
	}
	return nil
}

func sendDiscord(ctx context.Context, settings Settings, results []Result) error {
	client, err := NewRDAPClient(settings)
	if err != nil {
		return err
	}
	payload := map[string]string{"content": notificationMessage(results)}
	body, _ := json.Marshal(payload)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, settings.DiscordWebhookURL, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", settings.UserAgent)

	resp, err := client.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("send Discord webhook: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("send Discord webhook: HTTP %d", resp.StatusCode)
	}
	return nil
}

func sendTelegram(ctx context.Context, settings Settings, results []Result) error {
	client, err := NewRDAPClient(settings)
	if err != nil {
		return err
	}

	form := url.Values{}
	form.Set("chat_id", settings.TelegramChatID)
	form.Set("text", notificationMessage(results))
	form.Set("disable_web_page_preview", "true")

	endpoint := "https://api.telegram.org/bot" + url.PathEscape(settings.TelegramBotToken) + "/sendMessage"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("User-Agent", settings.UserAgent)

	resp, err := client.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("send Telegram message: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("send Telegram message: HTTP %d", resp.StatusCode)
	}
	return nil
}

func notificationMessage(results []Result) string {
	counts := CountResults(results)
	return fmt.Sprintf(
		"QDA finished: %d total, %d available, %d registered, %d reserved, %d premium, %d pending delete, %d redemption, %d rate limited, %d unknown.",
		len(results),
		counts[AvailabilityAvailable],
		counts[AvailabilityRegistered],
		counts[AvailabilityReserved],
		counts[AvailabilityPremium],
		counts[AvailabilityPendingDelete],
		counts[AvailabilityRedemption],
		counts[AvailabilityRateLimited],
		counts[AvailabilityUnknown],
	)
}
