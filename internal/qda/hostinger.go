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
	"sync"
	"time"

	"golang.org/x/net/publicsuffix"
)

const hostingerSourceName = "hostinger"

type HostingerSource struct {
	httpClient  *http.Client
	settings    Settings
	mu          sync.Mutex
	accounts    []hostingerAccountState
	nextAccount int
}

type hostingerAccountState struct {
	account HostingerAccount
	backoff sourceBackoff
}

type hostingerAvailabilityRequest struct {
	Domain           string   `json:"domain"`
	TLDs             []string `json:"tlds"`
	WithAlternatives bool     `json:"with_alternatives"`
}

type hostingerAvailabilityResult struct {
	Domain        *string `json:"domain"`
	IsAvailable   bool    `json:"is_available"`
	IsAlternative bool    `json:"is_alternative"`
	Restriction   *string `json:"restriction"`
}

func NewHostingerSource(settings Settings) (*HostingerSource, error) {
	client, err := newHTTPClient(settings)
	if err != nil {
		return nil, err
	}
	return &HostingerSource{
		httpClient: client,
		settings:   settings,
		accounts:   hostingerAccountStates(settings),
	}, nil
}

func (s *HostingerSource) Enabled() bool {
	return len(s.accounts) > 0
}

func (s *HostingerSource) Check(ctx context.Context, target Target) SourceResult {
	now := time.Now().UTC()
	if !s.Enabled() {
		return SourceResult{
			Name:         hostingerSourceName,
			Availability: AvailabilityUnknown,
			Lifecycle:    "hostinger_disabled",
			Confidence:   "unknown",
			Error:        "Hostinger fallback disabled: [hostinger] api_token is empty",
			CheckedAt:    now,
		}
	}

	domainName, tld, err := splitDomainForAvailability(target.Domain)
	if err != nil {
		return SourceResult{
			Name:         hostingerSourceName,
			Availability: AvailabilityUnknown,
			Lifecycle:    "unknown",
			Confidence:   "unknown",
			Error:        err.Error(),
			CheckedAt:    now,
		}
	}

	body, err := json.Marshal(hostingerAvailabilityRequest{
		Domain:           domainName,
		TLDs:             []string{tld},
		WithAlternatives: false,
	})
	if err != nil {
		return hostingerError("encode request: "+err.Error(), 0, now)
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
			return hostingerRateLimited(lastRateLimitBody, http.StatusTooManyRequests, wait, time.Now().UTC())
		}
		triedAccounts[accountIndex] = true

		endpoint := strings.TrimRight(s.settings.HostingerAPIBaseURL, "/") + "/api/domains/v1/availability"
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
		if err != nil {
			return hostingerError("build request: "+err.Error(), 0, now)
		}
		s.setHeaders(req, account)

		resp, err := s.httpClient.Do(req)
		if err != nil {
			return hostingerError("request failed: "+err.Error(), 0, now)
		}

		responseBody, err := io.ReadAll(io.LimitReader(resp.Body, maxRDAPBodyBytes))
		_ = resp.Body.Close()
		if err != nil {
			return hostingerError("read response: "+err.Error(), resp.StatusCode, now)
		}
		if len(responseBody) == maxRDAPBodyBytes {
			return hostingerError("response too large", resp.StatusCode, now)
		}
		if resp.StatusCode == http.StatusTooManyRequests {
			lastRetryAfter = clampRetryAfter(retryAfterFromHeader(resp.Header, now, s.settings.HostingerRateLimit), s.settings.SourceRateLimitMaxDelay)
			lastRateLimitBody = strings.TrimSpace(string(responseBody))
			s.postponeAccount(accountIndex, time.Now().UTC(), lastRetryAfter)
			continue
		}
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			return hostingerError(fmt.Sprintf("HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(responseBody))), resp.StatusCode, now)
		}

		var decoded []hostingerAvailabilityResult
		if err := json.Unmarshal(responseBody, &decoded); err != nil {
			return hostingerError("invalid JSON: "+err.Error(), resp.StatusCode, now)
		}

		expected := strings.ToLower(target.Domain)
		for _, item := range decoded {
			if item.Domain == nil || strings.ToLower(*item.Domain) != expected {
				continue
			}
			available := item.IsAvailable
			restriction := ""
			if item.Restriction != nil {
				restriction = *item.Restriction
			}
			availability := AvailabilityReserved
			lifecycle := "not_available"
			if available {
				availability = AvailabilityAvailable
				lifecycle = "available"
			} else if restriction != "" {
				lifecycle = "restricted"
			}
			return SourceResult{
				Name:         hostingerSourceName,
				Availability: availability,
				Lifecycle:    lifecycle,
				Confidence:   "hostinger_authoritative",
				Source:       endpoint,
				HTTPStatus:   resp.StatusCode,
				CheckedAt:    now,
				Registrable:  &available,
				Reason:       restriction,
			}
		}

		return SourceResult{
			Name:         hostingerSourceName,
			Availability: AvailabilityUnknown,
			Lifecycle:    "unknown",
			Confidence:   "unknown",
			Source:       endpoint,
			HTTPStatus:   resp.StatusCode,
			Error:        "Hostinger omitted domain from response",
			CheckedAt:    time.Now().UTC(),
		}
	}

	return hostingerRateLimited(lastRateLimitBody, http.StatusTooManyRequests, lastRetryAfter, time.Now().UTC())
}

func hostingerError(message string, httpStatus int, checkedAt time.Time) SourceResult {
	return SourceResult{
		Name:         hostingerSourceName,
		Availability: AvailabilityUnknown,
		Lifecycle:    "unknown",
		Confidence:   "unknown",
		HTTPStatus:   httpStatus,
		Error:        message,
		CheckedAt:    checkedAt,
	}
}

func hostingerRateLimited(body string, httpStatus int, retryAfter time.Duration, checkedAt time.Time) SourceResult {
	message := "rate limited"
	if body != "" {
		message += ": " + body
	}
	result := hostingerError(message, httpStatus, checkedAt)
	result.Availability = AvailabilityRateLimited
	result.Lifecycle = "rate_limited"
	result.RetryAfter = retryAfter
	result.RetryAfterSet = true
	return result
}

func (s *HostingerSource) setHeaders(req *http.Request, account HostingerAccount) {
	req.Header.Set("Authorization", "Bearer "+account.APIToken)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", s.settings.UserAgent)
}

func (s *HostingerSource) nextReadyAccount(now time.Time, tried map[int]bool) (int, HostingerAccount, time.Duration, bool) {
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
	return -1, HostingerAccount{}, wait, false
}

func (s *HostingerSource) postponeAccount(index int, now time.Time, retryAfter time.Duration) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if index < 0 || index >= len(s.accounts) {
		return
	}
	s.accounts[index].backoff.Postpone(now, retryAfter)
}

func hostingerAccountStates(settings Settings) []hostingerAccountState {
	accounts := normalizedHostingerAccounts(settings)
	out := make([]hostingerAccountState, 0, len(accounts))
	for _, account := range accounts {
		out = append(out, hostingerAccountState{account: account})
	}
	return out
}

func normalizedHostingerAccounts(settings Settings) []HostingerAccount {
	var out []HostingerAccount
	if strings.TrimSpace(settings.HostingerAPIToken) != "" {
		out = append(out, HostingerAccount{
			Name:     "primary",
			APIToken: strings.TrimSpace(settings.HostingerAPIToken),
		})
	}
	for _, account := range settings.HostingerAccounts {
		token := strings.TrimSpace(account.APIToken)
		if token == "" {
			continue
		}
		name := strings.TrimSpace(account.Name)
		if name == "" {
			name = fmt.Sprintf("account-%d", len(out)+1)
		}
		out = append(out, HostingerAccount{
			Name:     name,
			APIToken: token,
		})
	}
	return out
}

func splitDomainForAvailability(domain string) (string, string, error) {
	domain = strings.ToLower(strings.TrimSuffix(domain, "."))
	suffix, _ := publicsuffix.PublicSuffix(domain)
	if suffix == "" || suffix == domain {
		left, right, ok := strings.Cut(domain, ".")
		if !ok || left == "" || right == "" {
			return "", "", fmt.Errorf("cannot split domain for Hostinger availability: %s", domain)
		}
		return left, right, nil
	}
	prefix := strings.TrimSuffix(domain, "."+suffix)
	if prefix == "" || strings.Contains(prefix, ".") {
		return "", "", fmt.Errorf("domain is not registrable root for Hostinger availability: %s", domain)
	}
	if _, err := url.Parse("https://" + domain); err != nil {
		return "", "", fmt.Errorf("invalid domain for Hostinger availability: %w", err)
	}
	return prefix, suffix, nil
}
