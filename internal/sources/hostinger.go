package sources

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"golang.org/x/net/publicsuffix"

	"qda/internal/config"
	"qda/internal/httpkit"
	"qda/internal/types"
)

// Hostinger checks domains through the Hostinger availability API with
// per-account rotation and backoff.
type Hostinger struct {
	settings   config.Settings
	httpClient *http.Client
	mu         sync.Mutex
	accounts   []hostingerAccountState
	next       int
}

type hostingerAccountState struct {
	account     config.HostingerAccount
	frozenUntil time.Time
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

// NewHostinger creates the Hostinger source from settings.
func NewHostinger(settings config.Settings) *Hostinger {
	client, err := httpkit.NewClient(settings.Timeout, settings.Proxies, settings.ProxyFile)
	if err != nil {
		client = &http.Client{Timeout: settings.Timeout}
	}
	accounts := []hostingerAccountState{}
	if token := strings.TrimSpace(settings.Hostinger.APIToken); token != "" {
		accounts = append(accounts, hostingerAccountState{account: config.HostingerAccount{
			Name:     "primary",
			APIToken: token,
		}})
	}
	for i, account := range settings.Hostinger.Accounts {
		token := strings.TrimSpace(account.APIToken)
		if token == "" {
			continue
		}
		name := strings.TrimSpace(account.Name)
		if name == "" {
			name = fmt.Sprintf("account-%d", i+2)
		}
		accounts = append(accounts, hostingerAccountState{account: config.HostingerAccount{
			Name:     name,
			APIToken: token,
		}})
	}
	return &Hostinger{settings: settings, httpClient: client, accounts: accounts}
}

// Name identifies the source.
func (s *Hostinger) Name() string { return "hostinger" }

// Enabled reports whether at least one account is configured.
func (s *Hostinger) Enabled() bool { return len(s.accounts) > 0 }

// Check queries one domain, rotating accounts on rate limits.
func (s *Hostinger) Check(ctx context.Context, domain string) types.SourceResult {
	now := time.Now().UTC()

	domainName, tld, err := splitDomainForAvailability(domain)
	if err != nil {
		return hostingerError(err.Error(), 0, now)
	}

	body, err := json.Marshal(hostingerAvailabilityRequest{
		Domain:           domainName,
		TLDs:             []string{tld},
		WithAlternatives: false,
	})
	if err != nil {
		return hostingerError("encode request: "+err.Error(), 0, now)
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
			return hostingerRateLimited(lastRateLimitBody, http.StatusTooManyRequests, wait, now)
		}
		tried[accountIndex] = true

		endpoint := strings.TrimRight(s.settings.Hostinger.APIBaseURL, "/") + "/api/domains/v1/availability"
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
			result := hostingerError(httpkit.FailureMessage(err), 0, now)
			result.Retryable = true
			return result
		}
		if len(responseBody) == httpkit.MaxBodyBytes {
			return hostingerError("response too large", resp.StatusCode, now)
		}
		if resp.StatusCode == http.StatusTooManyRequests {
			retryAfter, _ := httpkit.RetryAfterFromHeader(resp.Header, now, s.settings.Hostinger.RateLimit)
			lastRetryAfter = httpkit.ClampRetryAfter(retryAfter, s.settings.SourceMaxDelay)
			lastRateLimitBody = strings.TrimSpace(string(responseBody))
			s.freezeAccount(accountIndex, time.Now(), lastRetryAfter)
			continue
		}
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			return hostingerError(fmt.Sprintf("HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(responseBody))), resp.StatusCode, now)
		}

		var decoded []hostingerAvailabilityResult
		if err := json.Unmarshal(responseBody, &decoded); err != nil {
			return hostingerError("invalid JSON: "+err.Error(), resp.StatusCode, now)
		}

		expected := strings.ToLower(domain)
		for _, item := range decoded {
			if item.Domain == nil || strings.ToLower(*item.Domain) != expected {
				continue
			}
			available := item.IsAvailable
			restriction := ""
			if item.Restriction != nil {
				restriction = *item.Restriction
			}
			availability := types.AvailabilityReserved
			lifecycle := "not_available"
			if available {
				availability = types.AvailabilityAvailable
				lifecycle = "available"
			} else if restriction != "" {
				lifecycle = "restricted"
			}
			return types.SourceResult{
				Name:         "hostinger",
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

		return hostingerError("Hostinger omitted domain from response", resp.StatusCode, now)
	}

	return hostingerRateLimited(lastRateLimitBody, http.StatusTooManyRequests, lastRetryAfter, now)
}

func (s *Hostinger) setHeaders(req *http.Request, account config.HostingerAccount) {
	req.Header.Set("Authorization", "Bearer "+account.APIToken)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", s.settings.UserAgent)
}

func (s *Hostinger) nextReadyAccount(now time.Time, tried map[int]bool) (int, config.HostingerAccount, time.Duration, bool) {
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
	return -1, config.HostingerAccount{}, wait, false
}

func (s *Hostinger) freezeAccount(index int, now time.Time, retryAfter time.Duration) {
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

func hostingerError(message string, httpStatus int, checkedAt time.Time) types.SourceResult {
	return types.SourceResult{
		Name:         "hostinger",
		Availability: types.AvailabilityUnknown,
		Lifecycle:    "unknown",
		Confidence:   "unknown",
		HTTPStatus:   httpStatus,
		Error:        message,
		CheckedAt:    checkedAt,
	}
}

func hostingerRateLimited(body string, httpStatus int, retryAfter time.Duration, checkedAt time.Time) types.SourceResult {
	message := "rate limited"
	if body != "" {
		message += ": " + body
	}
	result := hostingerError(message, httpStatus, checkedAt)
	result.Availability = types.AvailabilityRateLimited
	result.Lifecycle = "rate_limited"
	result.RetryAfter = retryAfter
	result.RetryAfterSet = true
	return result
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
