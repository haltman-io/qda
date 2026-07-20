package sources

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"qda/internal/config"
	"qda/internal/httpkit"
	"qda/internal/types"
)

// Vercel checks domains through the Vercel Registrar availability API with
// per-account rotation and backoff.
type Vercel struct {
	settings   config.Settings
	httpClient *http.Client
	mu         sync.Mutex
	accounts   []vercelAccountState
	next       int
}

type vercelAccountState struct {
	account     config.VercelAccount
	frozenUntil time.Time
}

type vercelAvailabilityRequest struct {
	Domains []string `json:"domains"`
}

type vercelBulkAvailabilityResponse struct {
	Results []vercelAvailabilityResult `json:"results"`
}

type vercelAvailabilityResult struct {
	Domain    string `json:"domain"`
	Available bool   `json:"available"`
}

type vercelPriceResponse struct {
	Years         json.RawMessage `json:"years"`
	PurchasePrice json.RawMessage `json:"purchasePrice"`
	RenewalPrice  json.RawMessage `json:"renewalPrice"`
	TransferPrice json.RawMessage `json:"transferPrice"`
}

// NewVercel creates the Vercel source from settings.
func NewVercel(settings config.Settings) *Vercel {
	client, err := httpkit.NewClient(settings.Timeout, settings.Proxies, settings.ProxyFile)
	if err != nil {
		client = &http.Client{Timeout: settings.Timeout}
	}
	accounts := []vercelAccountState{}
	if token := strings.TrimSpace(settings.Vercel.APIToken); token != "" {
		accounts = append(accounts, vercelAccountState{account: config.VercelAccount{
			Name:     "primary",
			APIToken: token,
			TeamID:   strings.TrimSpace(settings.Vercel.TeamID),
		}})
	}
	for i, account := range settings.Vercel.Accounts {
		token := strings.TrimSpace(account.APIToken)
		if token == "" {
			continue
		}
		name := strings.TrimSpace(account.Name)
		if name == "" {
			name = fmt.Sprintf("account-%d", i+2)
		}
		accounts = append(accounts, vercelAccountState{account: config.VercelAccount{
			Name:     name,
			APIToken: token,
			TeamID:   strings.TrimSpace(account.TeamID),
		}})
	}
	return &Vercel{settings: settings, httpClient: client, accounts: accounts}
}

// Name identifies the source.
func (s *Vercel) Name() string { return "vercel" }

// Enabled reports whether at least one account is configured.
func (s *Vercel) Enabled() bool { return len(s.accounts) > 0 }

// Check queries one domain, rotating accounts on rate limits.
func (s *Vercel) Check(ctx context.Context, domain string) types.SourceResult {
	now := time.Now().UTC()
	body, err := json.Marshal(vercelAvailabilityRequest{Domains: []string{domain}})
	if err != nil {
		return vercelError("encode request: "+err.Error(), 0, now)
	}

	tried := map[int]bool{}
	var lastRetryAfter time.Duration
	var lastRateLimitBody string
	for len(tried) < len(s.accounts) {
		accountIndex, account, wait, ok := s.nextReadyAccount(time.Now(), tried)
		if !ok {
			if wait <= 0 {
				wait = lastRetryAfter
			}
			return vercelRateLimited(lastRateLimitBody, http.StatusTooManyRequests, wait, now)
		}
		tried[accountIndex] = true

		endpoint, err := s.endpoint("/v1/registrar/domains/availability", nil, account)
		if err != nil {
			return vercelError("build endpoint: "+err.Error(), 0, now)
		}
		resp, responseBody, err := httpkit.Do(ctx, s.httpClient, httpkit.RetryConfig{
			Retries: s.settings.NetworkRetries,
			Delay:   s.settings.NetworkRetryDelay,
		}, func() (*http.Request, error) {
			req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
			if err != nil {
				return nil, err
			}
			s.setHeaders(req, account)
			return req, nil
		})
		if err != nil {
			result := vercelError(httpkit.FailureMessage(err), 0, now)
			result.Retryable = true
			return result
		}
		if len(responseBody) == httpkit.MaxBodyBytes {
			return vercelError("response too large", resp.StatusCode, now)
		}
		if resp.StatusCode == http.StatusTooManyRequests {
			retryAfter, _ := httpkit.RetryAfterFromHeader(resp.Header, now, s.settings.Vercel.RateLimit)
			lastRetryAfter = httpkit.ClampRetryAfter(retryAfter, s.settings.SourceMaxDelay)
			lastRateLimitBody = strings.TrimSpace(string(responseBody))
			s.freezeAccount(accountIndex, time.Now(), lastRetryAfter)
			continue
		}
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			return vercelError(fmt.Sprintf("HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(responseBody))), resp.StatusCode, now)
		}

		var decoded vercelBulkAvailabilityResponse
		if err := json.Unmarshal(responseBody, &decoded); err != nil {
			return vercelError("invalid JSON: "+err.Error(), resp.StatusCode, now)
		}

		for _, item := range decoded.Results {
			itemDomain := strings.ToLower(strings.TrimSuffix(item.Domain, "."))
			if itemDomain != strings.ToLower(domain) {
				continue
			}
			available := item.Available
			availability := types.AvailabilityReserved
			lifecycle := "not_available"
			if available {
				availability = types.AvailabilityAvailable
				lifecycle = "available"
			}
			result := types.SourceResult{
				Name:         "vercel",
				Availability: availability,
				Lifecycle:    lifecycle,
				Confidence:   "vercel_authoritative",
				Source:       endpoint,
				HTTPStatus:   resp.StatusCode,
				CheckedAt:    now,
				Registrable:  &available,
			}
			if available && s.settings.Vercel.FetchPrice {
				result.Pricing = s.getPrice(ctx, domain, account)
			}
			return result
		}

		return vercelError("Vercel omitted domain from response", resp.StatusCode, now)
	}

	return vercelRateLimited(lastRateLimitBody, http.StatusTooManyRequests, lastRetryAfter, now)
}

func (s *Vercel) getPrice(ctx context.Context, domain string, account config.VercelAccount) *types.Pricing {
	query := url.Values{}
	if s.settings.Vercel.PriceYears != "" {
		query.Set("years", s.settings.Vercel.PriceYears)
	}

	endpoint, err := s.endpoint("/v1/registrar/domains/"+url.PathEscape(domain)+"/price", query, account)
	if err != nil {
		return nil
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil
	}
	s.setHeaders(req, account)

	resp, err := s.httpClient.Do(req)
	if err != nil {
		return nil
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil
	}

	responseBody, err := io.ReadAll(io.LimitReader(resp.Body, httpkit.MaxBodyBytes))
	if err != nil || len(responseBody) == httpkit.MaxBodyBytes {
		return nil
	}

	var decoded vercelPriceResponse
	if err := json.Unmarshal(responseBody, &decoded); err != nil {
		return nil
	}

	return &types.Pricing{
		RegistrationCost: jsonPriceString(decoded.PurchasePrice),
		RenewalCost:      jsonPriceString(decoded.RenewalPrice),
		TransferCost:     jsonPriceString(decoded.TransferPrice),
		Years:            jsonPriceString(decoded.Years),
	}
}

func (s *Vercel) endpoint(path string, extraQuery url.Values, account config.VercelAccount) (string, error) {
	base, err := url.Parse(strings.TrimRight(s.settings.Vercel.APIBaseURL, "/"))
	if err != nil {
		return "", err
	}
	joined, err := url.Parse(strings.TrimLeft(path, "/"))
	if err != nil {
		return "", err
	}
	resolved := base.ResolveReference(joined)
	query := resolved.Query()
	if strings.TrimSpace(account.TeamID) != "" {
		query.Set("teamId", strings.TrimSpace(account.TeamID))
	}
	for key, values := range extraQuery {
		for _, value := range values {
			query.Add(key, value)
		}
	}
	resolved.RawQuery = query.Encode()
	return resolved.String(), nil
}

func (s *Vercel) setHeaders(req *http.Request, account config.VercelAccount) {
	req.Header.Set("Authorization", "Bearer "+account.APIToken)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", s.settings.UserAgent)
}

func (s *Vercel) nextReadyAccount(now time.Time, tried map[int]bool) (int, config.VercelAccount, time.Duration, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()

	var wait time.Duration
	waitSet := false
	for offset := 0; offset < len(s.accounts); offset++ {
		index := (s.next + offset) % len(s.accounts)
		if tried[index] {
			continue
		}
		if frozen := s.accounts[index].frozenUntil; frozen.After(now) {
			delay := frozen.Sub(now)
			if !waitSet || delay < wait {
				wait = delay
				waitSet = true
			}
			continue
		}
		s.next = (index + 1) % len(s.accounts)
		return index, s.accounts[index].account, 0, true
	}
	return -1, config.VercelAccount{}, wait, false
}

func (s *Vercel) freezeAccount(index int, now time.Time, retryAfter time.Duration) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if index < 0 || index >= len(s.accounts) {
		return
	}
	until := now.Add(retryAfter)
	if until.After(s.accounts[index].frozenUntil) {
		s.accounts[index].frozenUntil = until
	}
}

func vercelError(message string, httpStatus int, checkedAt time.Time) types.SourceResult {
	return types.SourceResult{
		Name:         "vercel",
		Availability: types.AvailabilityUnknown,
		Lifecycle:    "unknown",
		Confidence:   "unknown",
		HTTPStatus:   httpStatus,
		Error:        message,
		CheckedAt:    checkedAt,
	}
}

func vercelRateLimited(body string, httpStatus int, retryAfter time.Duration, checkedAt time.Time) types.SourceResult {
	message := "rate limited"
	if body != "" {
		message += ": " + body
	}
	result := vercelError(message, httpStatus, checkedAt)
	result.Availability = types.AvailabilityRateLimited
	result.Lifecycle = "rate_limited"
	result.RetryAfter = retryAfter
	result.RetryAfterSet = true
	return result
}

func jsonPriceString(raw json.RawMessage) string {
	if len(raw) == 0 || string(raw) == "null" {
		return ""
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return s
	}
	var number json.Number
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if err := decoder.Decode(&number); err == nil {
		if _, err := number.Int64(); err == nil {
			return number.String()
		}
		if f, err := strconv.ParseFloat(number.String(), 64); err == nil {
			return strconv.FormatFloat(f, 'f', -1, 64)
		}
		return number.String()
	}
	return ""
}
