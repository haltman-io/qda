package qda

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
)

const vercelSourceName = "vercel"

type VercelSource struct {
	httpClient  *http.Client
	settings    Settings
	mu          sync.Mutex
	accounts    []vercelAccountState
	nextAccount int
}

type vercelAccountState struct {
	account VercelAccount
	backoff sourceBackoff
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

func NewVercelSource(settings Settings) (*VercelSource, error) {
	client, err := newHTTPClient(settings)
	if err != nil {
		return nil, err
	}
	return &VercelSource{
		httpClient: client,
		settings:   settings,
		accounts:   vercelAccountStates(settings),
	}, nil
}

func (s *VercelSource) Enabled() bool {
	return len(s.accounts) > 0
}

func (s *VercelSource) Check(ctx context.Context, targets []Target) map[string]SourceResult {
	out := map[string]SourceResult{}
	if !s.Enabled() {
		now := time.Now().UTC()
		for _, target := range targets {
			out[target.Domain] = SourceResult{
				Name:         vercelSourceName,
				Availability: AvailabilityUnknown,
				Lifecycle:    "vercel_disabled",
				Confidence:   "unknown",
				Error:        "Vercel fallback disabled: [vercel] api_token is empty",
				CheckedAt:    now,
			}
		}
		return out
	}

	batchSize := s.settings.VercelBatchSize
	if batchSize < 1 || batchSize > 50 {
		batchSize = 50
	}

	for start := 0; start < len(targets); start += batchSize {
		if start > 0 && s.settings.VercelRateLimit > 0 {
			timer := time.NewTimer(s.settings.VercelRateLimit)
			select {
			case <-timer.C:
			case <-ctx.Done():
				timer.Stop()
				for _, target := range targets[start:] {
					out[target.Domain] = vercelError("request cancelled: "+ctx.Err().Error(), 0, time.Now().UTC())
				}
				return out
			}
		}

		end := start + batchSize
		if end > len(targets) {
			end = len(targets)
		}
		for domain, result := range s.checkBatch(ctx, targets[start:end]) {
			out[domain] = result
		}
	}

	return out
}

func (s *VercelSource) checkBatch(ctx context.Context, targets []Target) map[string]SourceResult {
	now := time.Now().UTC()
	out := map[string]SourceResult{}
	for _, target := range targets {
		out[target.Domain] = SourceResult{
			Name:         vercelSourceName,
			Availability: AvailabilityUnknown,
			Lifecycle:    "unknown",
			Confidence:   "unknown",
			Error:        "Vercel omitted domain from response",
			CheckedAt:    now,
		}
	}

	domains := make([]string, 0, len(targets))
	for _, target := range targets {
		domains = append(domains, target.Domain)
	}

	body, err := json.Marshal(vercelAvailabilityRequest{Domains: domains})
	if err != nil {
		return vercelBatchError(targets, "encode request: "+err.Error(), 0)
	}

	triedAccounts := map[int]bool{}
	var lastRetryAfter time.Duration
	var lastRateLimitBody string
	for len(triedAccounts) < len(s.accounts) {
		accountIndex, account, wait, ok := s.nextReadyAccount(time.Now().UTC(), triedAccounts)
		if !ok {
			if wait <= 0 {
				wait = lastRetryAfter
			}
			return vercelRateLimited(targets, lastRateLimitBody, http.StatusTooManyRequests, wait)
		}
		triedAccounts[accountIndex] = true

		endpoint, err := s.endpoint("/v1/registrar/domains/availability", nil, account)
		if err != nil {
			return vercelBatchError(targets, "build endpoint: "+err.Error(), 0)
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
		if err != nil {
			return vercelBatchError(targets, "build request: "+err.Error(), 0)
		}
		s.setHeaders(req, account)

		resp, err := s.httpClient.Do(req)
		if err != nil {
			return vercelBatchError(targets, "request failed: "+err.Error(), 0)
		}

		responseBody, err := io.ReadAll(io.LimitReader(resp.Body, maxRDAPBodyBytes))
		_ = resp.Body.Close()
		if err != nil {
			return vercelBatchError(targets, "read response: "+err.Error(), resp.StatusCode)
		}
		if len(responseBody) == maxRDAPBodyBytes {
			return vercelBatchError(targets, "response too large", resp.StatusCode)
		}
		if resp.StatusCode == http.StatusTooManyRequests {
			lastRetryAfter = clampRetryAfter(retryAfterFromHeader(resp.Header, now, s.settings.VercelRateLimit), s.settings.SourceRateLimitMaxDelay)
			lastRateLimitBody = strings.TrimSpace(string(responseBody))
			s.postponeAccount(accountIndex, time.Now().UTC(), lastRetryAfter)
			continue
		}
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			return vercelBatchError(targets, fmt.Sprintf("HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(responseBody))), resp.StatusCode)
		}

		var decoded vercelBulkAvailabilityResponse
		if err := json.Unmarshal(responseBody, &decoded); err != nil {
			return vercelBatchError(targets, "invalid JSON: "+err.Error(), resp.StatusCode)
		}

		for _, item := range decoded.Results {
			domain := strings.ToLower(strings.TrimSuffix(item.Domain, "."))
			available := item.Available
			availability := AvailabilityReserved
			lifecycle := "not_available"
			if available {
				availability = AvailabilityAvailable
				lifecycle = "available"
			}
			result := SourceResult{
				Name:         vercelSourceName,
				Availability: availability,
				Lifecycle:    lifecycle,
				Confidence:   "vercel_authoritative",
				Source:       endpoint,
				HTTPStatus:   resp.StatusCode,
				CheckedAt:    now,
				Registrable:  &available,
			}
			if available && s.settings.VercelFetchPrice {
				result.Pricing = s.getPrice(ctx, domain, account)
			}
			out[domain] = result
		}

		return out
	}

	return vercelRateLimited(targets, lastRateLimitBody, http.StatusTooManyRequests, lastRetryAfter)
}

func (s *VercelSource) getPrice(ctx context.Context, domain string, account VercelAccount) *Pricing {
	query := url.Values{}
	if s.settings.VercelPriceYears != "" {
		query.Set("years", s.settings.VercelPriceYears)
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

	responseBody, err := io.ReadAll(io.LimitReader(resp.Body, maxRDAPBodyBytes))
	if err != nil || len(responseBody) == maxRDAPBodyBytes {
		return nil
	}

	var decoded vercelPriceResponse
	if err := json.Unmarshal(responseBody, &decoded); err != nil {
		return nil
	}

	return &Pricing{
		RegistrationCost: jsonPriceString(decoded.PurchasePrice),
		RenewalCost:      jsonPriceString(decoded.RenewalPrice),
		TransferCost:     jsonPriceString(decoded.TransferPrice),
		Years:            jsonPriceString(decoded.Years),
	}
}

func (s *VercelSource) endpoint(path string, extraQuery url.Values, account VercelAccount) (string, error) {
	base, err := url.Parse(strings.TrimRight(s.settings.VercelAPIBaseURL, "/"))
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

func (s *VercelSource) setHeaders(req *http.Request, account VercelAccount) {
	req.Header.Set("Authorization", "Bearer "+account.APIToken)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", s.settings.UserAgent)
}

func (s *VercelSource) nextReadyAccount(now time.Time, tried map[int]bool) (int, VercelAccount, time.Duration, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()

	var wait time.Duration
	waitSet := false
	for offset := 0; offset < len(s.accounts); offset++ {
		index := (s.nextAccount + offset) % len(s.accounts)
		if tried[index] {
			continue
		}
		delay := s.accounts[index].backoff.Delay(now)
		if delay <= 0 {
			s.nextAccount = (index + 1) % len(s.accounts)
			return index, s.accounts[index].account, 0, true
		}
		if !waitSet || delay < wait {
			wait = delay
			waitSet = true
		}
	}
	return -1, VercelAccount{}, wait, false
}

func (s *VercelSource) postponeAccount(index int, now time.Time, retryAfter time.Duration) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if index < 0 || index >= len(s.accounts) {
		return
	}
	s.accounts[index].backoff.Postpone(now, retryAfter)
}

func vercelAccountStates(settings Settings) []vercelAccountState {
	accounts := normalizedVercelAccounts(settings)
	out := make([]vercelAccountState, 0, len(accounts))
	for _, account := range accounts {
		out = append(out, vercelAccountState{account: account})
	}
	return out
}

func normalizedVercelAccounts(settings Settings) []VercelAccount {
	var out []VercelAccount
	if strings.TrimSpace(settings.VercelAPIToken) != "" {
		out = append(out, VercelAccount{
			Name:     "primary",
			APIToken: strings.TrimSpace(settings.VercelAPIToken),
			TeamID:   strings.TrimSpace(settings.VercelTeamID),
		})
	}
	for _, account := range settings.VercelAccounts {
		token := strings.TrimSpace(account.APIToken)
		if token == "" {
			continue
		}
		name := strings.TrimSpace(account.Name)
		if name == "" {
			name = fmt.Sprintf("account-%d", len(out)+1)
		}
		out = append(out, VercelAccount{
			Name:     name,
			APIToken: token,
			TeamID:   strings.TrimSpace(account.TeamID),
		})
	}
	return out
}

func vercelBatchError(targets []Target, message string, httpStatus int) map[string]SourceResult {
	now := time.Now().UTC()
	out := map[string]SourceResult{}
	for _, target := range targets {
		result := vercelError(message, httpStatus, now)
		out[target.Domain] = result
	}
	return out
}

func vercelRateLimited(targets []Target, body string, httpStatus int, retryAfter time.Duration) map[string]SourceResult {
	message := "rate limited"
	if body != "" {
		message += ": " + body
	}
	now := time.Now().UTC()
	out := map[string]SourceResult{}
	for _, target := range targets {
		result := vercelError(message, httpStatus, now)
		result.Availability = AvailabilityRateLimited
		result.Lifecycle = "rate_limited"
		result.RetryAfter = retryAfter
		result.RetryAfterSet = true
		out[target.Domain] = result
	}
	return out
}

func vercelError(message string, httpStatus int, checkedAt time.Time) SourceResult {
	return SourceResult{
		Name:         vercelSourceName,
		Availability: AvailabilityUnknown,
		Lifecycle:    "unknown",
		Confidence:   "unknown",
		HTTPStatus:   httpStatus,
		Error:        message,
		CheckedAt:    checkedAt,
	}
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
