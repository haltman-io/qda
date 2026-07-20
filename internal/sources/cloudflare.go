package sources

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"qda/internal/config"
	"qda/internal/httpkit"
	"qda/internal/types"
)

// Cloudflare checks domains through the Cloudflare Registrar domain-check API.
type Cloudflare struct {
	settings   config.Settings
	httpClient *http.Client
}

type cloudflareCheckRequest struct {
	Domains []string `json:"domains"`
}

type cloudflareCheckResponse struct {
	Success  bool                     `json:"success"`
	Errors   []cloudflareResponseInfo `json:"errors"`
	Result   struct {
		Domains []cloudflareDomainResult `json:"domains"`
	} `json:"result"`
}

type cloudflareResponseInfo struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type cloudflareDomainResult struct {
	Name        string         `json:"name"`
	Registrable bool           `json:"registrable"`
	Pricing     *types.Pricing `json:"pricing,omitempty"`
	Reason      string         `json:"reason,omitempty"`
	Tier        string         `json:"tier,omitempty"`
}

// NewCloudflare creates the Cloudflare source.
func NewCloudflare(settings config.Settings) *Cloudflare {
	client, err := httpkit.NewClient(settings.Timeout, settings.Proxies, settings.ProxyFile)
	if err != nil {
		return &Cloudflare{settings: settings, httpClient: &http.Client{Timeout: settings.Timeout}}
	}
	return &Cloudflare{settings: settings, httpClient: client}
}

// Name identifies the source.
func (s *Cloudflare) Name() string { return "cloudflare" }

// Enabled reports whether credentials are configured.
func (s *Cloudflare) Enabled() bool {
	return strings.TrimSpace(s.settings.Cloudflare.AccountID) != "" &&
		strings.TrimSpace(s.settings.Cloudflare.APIToken) != ""
}

// Check queries one domain.
func (s *Cloudflare) Check(ctx context.Context, domain string) types.SourceResult {
	now := time.Now().UTC()
	body, err := json.Marshal(cloudflareCheckRequest{Domains: []string{domain}})
	if err != nil {
		return cloudflareError("encode request: "+err.Error(), 0, now)
	}

	endpoint := strings.TrimRight(s.settings.Cloudflare.APIBaseURL, "/") +
		"/accounts/" + url.PathEscape(s.settings.Cloudflare.AccountID) +
		"/registrar/domain-check"
	resp, responseBody, err := httpkit.Do(ctx, s.httpClient, httpkit.RetryConfig{
		Retries: s.settings.NetworkRetries,
		Delay:   s.settings.NetworkRetryDelay,
	}, func() (*http.Request, error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
		if err != nil {
			return nil, err
		}
		req.Header.Set("Authorization", "Bearer "+s.settings.Cloudflare.APIToken)
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "application/json")
		req.Header.Set("User-Agent", s.settings.UserAgent)
		return req, nil
	})
	if err != nil {
		result := cloudflareError(httpkit.FailureMessage(err), 0, now)
		result.Retryable = true
		return result
	}
	if len(responseBody) == httpkit.MaxBodyBytes {
		return cloudflareError("response too large", resp.StatusCode, now)
	}
	if resp.StatusCode == http.StatusTooManyRequests {
		retryAfter, _ := httpkit.RetryAfterFromHeader(resp.Header, now, s.settings.Cloudflare.RateLimit)
		return cloudflareRateLimited(strings.TrimSpace(string(responseBody)), resp.StatusCode,
			httpkit.ClampRetryAfter(retryAfter, s.settings.SourceMaxDelay), now)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return cloudflareError(fmt.Sprintf("HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(responseBody))), resp.StatusCode, now)
	}

	var decoded cloudflareCheckResponse
	if err := json.Unmarshal(responseBody, &decoded); err != nil {
		return cloudflareError("invalid JSON: "+err.Error(), resp.StatusCode, now)
	}
	if !decoded.Success {
		return cloudflareError("API returned success=false: "+cloudflareErrors(decoded.Errors), resp.StatusCode, now)
	}

	for _, item := range decoded.Result.Domains {
		if !strings.EqualFold(item.Name, domain) {
			continue
		}
		availability := types.AvailabilityRegistered
		lifecycle := "not_registrable"
		confidence := "authoritative"
		if item.Registrable {
			availability = types.AvailabilityAvailable
			lifecycle = "available"
		} else {
			switch item.Reason {
			case "domain_premium":
				availability = types.AvailabilityPremium
				lifecycle = "premium"
			case "extension_disallows_registration":
				availability = types.AvailabilityReserved
				lifecycle = "extension_disallows_registration"
			case "extension_not_supported", "extension_not_supported_via_api":
				availability = types.AvailabilityUnknown
				lifecycle = item.Reason
				confidence = "unsupported_by_cloudflare"
			case "domain_unavailable":
				availability = types.AvailabilityReserved
				lifecycle = "not_registrable"
			default:
				availability = types.AvailabilityUnknown
				lifecycle = "unknown"
				confidence = "unknown"
			}
		}

		registrable := item.Registrable
		return types.SourceResult{
			Name:         "cloudflare",
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

	return cloudflareError("cloudflare omitted domain from response", resp.StatusCode, now)
}

func cloudflareError(message string, httpStatus int, checkedAt time.Time) types.SourceResult {
	return types.SourceResult{
		Name:         "cloudflare",
		Availability: types.AvailabilityUnknown,
		Lifecycle:    "unknown",
		Confidence:   "unknown",
		HTTPStatus:   httpStatus,
		Error:        message,
		CheckedAt:    checkedAt,
	}
}

func cloudflareRateLimited(body string, httpStatus int, retryAfter time.Duration, checkedAt time.Time) types.SourceResult {
	message := "rate limited"
	if body != "" {
		message += ": " + body
	}
	return types.SourceResult{
		Name:          "cloudflare",
		Availability:  types.AvailabilityRateLimited,
		Lifecycle:     "rate_limited",
		Confidence:    "unknown",
		HTTPStatus:    httpStatus,
		Error:         message,
		CheckedAt:     checkedAt,
		RetryAfter:    retryAfter,
		RetryAfterSet: true,
	}
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
