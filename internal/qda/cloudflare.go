package qda

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const cloudflareSourceName = "cloudflare"

type CloudflareSource struct {
	httpClient *http.Client
	settings   Settings
}

type cloudflareCheckRequest struct {
	Domains []string `json:"domains"`
}

type cloudflareCheckResponse struct {
	Success  bool                     `json:"success"`
	Errors   []cloudflareResponseInfo `json:"errors"`
	Messages []cloudflareResponseInfo `json:"messages"`
	Result   struct {
		Domains []cloudflareDomainResult `json:"domains"`
	} `json:"result"`
}

type cloudflareResponseInfo struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type cloudflareDomainResult struct {
	Name        string   `json:"name"`
	Registrable bool     `json:"registrable"`
	Pricing     *Pricing `json:"pricing,omitempty"`
	Reason      string   `json:"reason,omitempty"`
	Tier        string   `json:"tier,omitempty"`
}

func NewCloudflareSource(settings Settings) (*CloudflareSource, error) {
	client, err := newHTTPClient(settings)
	if err != nil {
		return nil, err
	}
	return &CloudflareSource{
		httpClient: client,
		settings:   settings,
	}, nil
}

func (s *CloudflareSource) Check(ctx context.Context, targets []Target) map[string]SourceResult {
	out := map[string]SourceResult{}
	batchSize := s.settings.CloudflareBatchSize
	if batchSize < 1 || batchSize > 20 {
		batchSize = 20
	}

	for start := 0; start < len(targets); start += batchSize {
		if start > 0 && s.settings.RateLimit > 0 {
			timer := time.NewTimer(s.settings.RateLimit)
			select {
			case <-timer.C:
			case <-ctx.Done():
				timer.Stop()
				for _, target := range targets[start:] {
					out[target.Domain] = SourceResult{
						Name:         cloudflareSourceName,
						Availability: AvailabilityUnknown,
						Lifecycle:    "unknown",
						Confidence:   "unknown",
						Error:        ctx.Err().Error(),
						CheckedAt:    time.Now().UTC(),
					}
				}
				return out
			}
		}
		end := start + batchSize
		if end > len(targets) {
			end = len(targets)
		}
		batch := targets[start:end]
		results := s.checkBatch(ctx, batch)
		for domain, result := range results {
			out[domain] = result
		}
	}

	return out
}

func (s *CloudflareSource) checkBatch(ctx context.Context, targets []Target) map[string]SourceResult {
	now := time.Now().UTC()
	out := map[string]SourceResult{}
	for _, target := range targets {
		out[target.Domain] = SourceResult{
			Name:         cloudflareSourceName,
			Availability: AvailabilityUnknown,
			Lifecycle:    "unknown",
			Confidence:   "unknown",
			CheckedAt:    now,
			Error:        "cloudflare omitted domain from response",
		}
	}

	domains := make([]string, 0, len(targets))
	for _, target := range targets {
		domains = append(domains, target.Domain)
	}

	body, err := json.Marshal(cloudflareCheckRequest{Domains: domains})
	if err != nil {
		return cloudflareBatchError(targets, "encode request: "+err.Error(), 0)
	}

	endpoint := strings.TrimRight(s.settings.CloudflareAPIBaseURL, "/") +
		"/accounts/" + url.PathEscape(s.settings.CloudflareAccountID) +
		"/registrar/domain-check"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return cloudflareBatchError(targets, "build request: "+err.Error(), 0)
	}
	req.Header.Set("Authorization", "Bearer "+s.settings.CloudflareAPIToken)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", s.settings.UserAgent)

	resp, err := s.httpClient.Do(req)
	if err != nil {
		return cloudflareBatchError(targets, "request failed: "+err.Error(), 0)
	}
	defer resp.Body.Close()

	responseBody, err := io.ReadAll(io.LimitReader(resp.Body, maxRDAPBodyBytes))
	if err != nil {
		return cloudflareBatchError(targets, "read response: "+err.Error(), resp.StatusCode)
	}
	if len(responseBody) == maxRDAPBodyBytes {
		return cloudflareBatchError(targets, "response too large", resp.StatusCode)
	}
	if resp.StatusCode == http.StatusTooManyRequests {
		retryAfter := retryAfterFromHeader(resp.Header, now, s.settings.RateLimit)
		return cloudflareRateLimited(targets, strings.TrimSpace(string(responseBody)), resp.StatusCode, clampRetryAfter(retryAfter, s.settings.SourceRateLimitMaxDelay))
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return cloudflareBatchError(targets, fmt.Sprintf("HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(responseBody))), resp.StatusCode)
	}

	var decoded cloudflareCheckResponse
	if err := json.Unmarshal(responseBody, &decoded); err != nil {
		return cloudflareBatchError(targets, "invalid JSON: "+err.Error(), resp.StatusCode)
	}
	if !decoded.Success {
		return cloudflareBatchError(targets, "API returned success=false: "+cloudflareErrors(decoded.Errors), resp.StatusCode)
	}

	for _, item := range decoded.Result.Domains {
		domain := strings.ToLower(item.Name)
		availability := AvailabilityRegistered
		lifecycle := "not_registrable"
		confidence := "authoritative"
		if item.Registrable {
			availability = AvailabilityAvailable
			lifecycle = "available"
		} else {
			switch item.Reason {
			case "domain_premium":
				availability = AvailabilityPremium
				lifecycle = "premium"
			case "extension_disallows_registration":
				availability = AvailabilityReserved
				lifecycle = "extension_disallows_registration"
			case "extension_not_supported", "extension_not_supported_via_api":
				availability = AvailabilityUnknown
				lifecycle = item.Reason
				confidence = "unsupported_by_cloudflare"
			case "domain_unavailable":
				availability = AvailabilityReserved
				lifecycle = "not_registrable"
			default:
				availability = AvailabilityUnknown
				lifecycle = "unknown"
				confidence = "unknown"
			}
		}

		registrable := item.Registrable
		out[domain] = SourceResult{
			Name:         cloudflareSourceName,
			Availability: availability,
			Lifecycle:    lifecycle,
			Confidence:   confidence,
			Source:       endpoint,
			HTTPStatus:   resp.StatusCode,
			CheckedAt:    now,
			Registrable:  &registrable,
			Reason:       item.Reason,
			Tier:         item.Tier,
			Pricing:      item.Pricing,
		}
	}

	return out
}

func cloudflareBatchError(targets []Target, message string, httpStatus int) map[string]SourceResult {
	now := time.Now().UTC()
	out := map[string]SourceResult{}
	for _, target := range targets {
		out[target.Domain] = SourceResult{
			Name:         cloudflareSourceName,
			Availability: AvailabilityUnknown,
			Lifecycle:    "unknown",
			Confidence:   "unknown",
			HTTPStatus:   httpStatus,
			Error:        message,
			CheckedAt:    now,
		}
	}
	return out
}

func cloudflareRateLimited(targets []Target, body string, httpStatus int, retryAfter time.Duration) map[string]SourceResult {
	message := "rate limited"
	if body != "" {
		message += ": " + body
	}
	now := time.Now().UTC()
	out := map[string]SourceResult{}
	for _, target := range targets {
		out[target.Domain] = SourceResult{
			Name:          cloudflareSourceName,
			Availability:  AvailabilityRateLimited,
			Lifecycle:     "rate_limited",
			Confidence:    "unknown",
			HTTPStatus:    httpStatus,
			Error:         message,
			CheckedAt:     now,
			RetryAfter:    retryAfter,
			RetryAfterSet: true,
		}
	}
	return out
}

func cloudflareErrors(errors []cloudflareResponseInfo) string {
	if len(errors) == 0 {
		return "no error detail"
	}
	parts := make([]string, 0, len(errors))
	for _, item := range errors {
		if item.Message != "" {
			parts = append(parts, fmt.Sprintf("%d %s", item.Code, item.Message))
		}
	}
	if len(parts) == 0 {
		return "no error detail"
	}
	return strings.Join(parts, "; ")
}
