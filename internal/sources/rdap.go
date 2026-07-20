package sources

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"qda/internal/config"
	"qda/internal/httpkit"
	"qda/internal/types"
)

const (
	nicbrRateLimitExceededHeader = "Nicbr-Rate-Limit-Exceeded"
	nicbrPermissionDeniedHeader  = "Nicbr-Permission-Denied"
)

// RDAPBootstrap is the IANA dns.json document.
type RDAPBootstrap struct {
	Services [][][]string `json:"services"`
}

// RDAPSource queries authoritative RDAP servers resolved through the IANA
// bootstrap.
type RDAPSource struct {
	settings   config.Settings
	httpClient *http.Client
	bootstrap  *RDAPBootstrap
	// BootstrapWarning carries a non-fatal bootstrap fallback message.
	BootstrapWarning string
}

type rdapDomainResponse struct {
	ObjectClassName string          `json:"objectClassName"`
	LDHName         string          `json:"ldhName"`
	UnicodeName     string          `json:"unicodeName"`
	Status          []string        `json:"status"`
	Events          []rdapEvent     `json:"events"`
	Entities        []rdapEntity    `json:"entities"`
	ErrorCode       int             `json:"errorCode"`
	Title           string          `json:"title"`
	Description     []string        `json:"description"`
}

type rdapEvent struct {
	EventAction string `json:"eventAction"`
	EventDate   string `json:"eventDate"`
}

type rdapEntity struct {
	Handle     string          `json:"handle"`
	Roles      []string        `json:"roles"`
	VCardArray json.RawMessage `json:"vcardArray"`
}

// NewRDAP creates an RDAP source.
func NewRDAP(settings config.Settings) (*RDAPSource, error) {
	client, err := httpkit.NewClient(settings.Timeout, settings.Proxies, settings.ProxyFile)
	if err != nil {
		return nil, err
	}
	return &RDAPSource{settings: settings, httpClient: client}, nil
}

// Name identifies the source.
func (s *RDAPSource) Name() string { return "rdap" }

// Load fetches (or loads from cache) the IANA RDAP bootstrap.
func (s *RDAPSource) Load(ctx context.Context) error {
	cachePath := s.settings.RDAP.BootstrapCachePath

	if !s.settings.RDAP.BootstrapRefresh && cachePath != "" {
		if bootstrap, ok := readFreshBootstrap(cachePath, 24*time.Hour); ok {
			s.bootstrap = bootstrap
			return nil
		}
	}

	resp, body, err := httpkit.Do(ctx, s.httpClient, s.retryConfig(), func() (*http.Request, error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.settings.RDAP.BootstrapURL, nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("Accept", "application/json")
		req.Header.Set("User-Agent", s.settings.UserAgent)
		return req, nil
	})
	if err != nil {
		if bootstrap, ok := readAnyBootstrap(cachePath); ok {
			s.bootstrap = bootstrap
			s.BootstrapWarning = "using cached RDAP bootstrap because refresh failed: " + httpkit.FailureMessage(err)
			return nil
		}
		return fmt.Errorf("load RDAP bootstrap: %s", httpkit.FailureMessage(err))
	}

	if resp.StatusCode != http.StatusOK {
		if bootstrap, ok := readAnyBootstrap(cachePath); ok {
			s.bootstrap = bootstrap
			s.BootstrapWarning = fmt.Sprintf("using cached RDAP bootstrap because refresh returned HTTP %d", resp.StatusCode)
			return nil
		}
		return fmt.Errorf("load RDAP bootstrap: HTTP %d", resp.StatusCode)
	}
	if len(body) == httpkit.MaxBodyBytes {
		return errors.New("load RDAP bootstrap: response too large")
	}

	bootstrap, err := parseBootstrap(body)
	if err != nil {
		return err
	}
	s.bootstrap = bootstrap

	if cachePath != "" {
		_ = os.MkdirAll(filepath.Dir(cachePath), 0o755)
		_ = os.WriteFile(cachePath, body, 0o644)
	}
	return nil
}

// KeyFor returns the rate-limit key for a domain based on its RDAP server
// (per-registry pacing: rdap.nic.br is limited separately from rdap.verisign.com).
func (s *RDAPSource) KeyFor(domain string) string {
	base := s.baseFor(domain)
	if base == "" {
		return "rdap"
	}
	if parsed, err := url.Parse(base); err == nil && parsed.Host != "" {
		return "rdap:" + parsed.Host
	}
	return "rdap"
}

// Check queries the RDAP server responsible for the domain once.
func (s *RDAPSource) Check(ctx context.Context, target types.Target) types.Result {
	now := time.Now().UTC()
	base := s.baseFor(target.Domain)
	if base == "" {
		return types.Result{
			Domain:       target.Domain,
			Input:        target.Input,
			LineNumber:   target.LineNumber,
			Availability: types.AvailabilityUnknown,
			Lifecycle:    "unknown",
			Confidence:   "none",
			Error:        "no RDAP bootstrap endpoint for this TLD",
			CheckedAt:    now,
		}
	}

	rdapURL := joinRDAPURL(base, target.Domain)
	resp, body, err := httpkit.Do(ctx, s.httpClient, s.retryConfig(), func() (*http.Request, error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, rdapURL, nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("Accept", "application/rdap+json, application/json")
		req.Header.Set("User-Agent", s.settings.UserAgent)
		return req, nil
	})
	if err != nil {
		result := unknownResult(target, rdapURL, 0, httpkit.FailureMessage(err), now)
		if !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded) {
			result.Retryable = true
		}
		return result
	}

	if headerFlagEnabled(resp.Header, nicbrPermissionDeniedHeader) {
		return s.rateLimitedResult(target, rdapURL, resp.StatusCode, "permission_denied",
			fmt.Sprintf("Registro.br RDAP permission denied (%s=%s); requests are blocked by NIC.br",
				nicbrPermissionDeniedHeader, resp.Header.Get(nicbrPermissionDeniedHeader)), resp.Header, now)
	}
	if headerFlagEnabled(resp.Header, nicbrRateLimitExceededHeader) {
		return s.rateLimitedResult(target, rdapURL, resp.StatusCode, "rate_limited",
			fmt.Sprintf("Registro.br RDAP rate limit exceeded (%s=%s)",
				nicbrRateLimitExceededHeader, resp.Header.Get(nicbrRateLimitExceededHeader)), resp.Header, now)
	}

	switch {
	case resp.StatusCode == http.StatusNotFound:
		return types.Result{
			Domain:       target.Domain,
			Input:        target.Input,
			LineNumber:   target.LineNumber,
			Availability: types.AvailabilityAvailable,
			Lifecycle:    "available",
			Confidence:   "authoritative",
			Source:       rdapURL,
			HTTPStatus:   resp.StatusCode,
			CheckedAt:    now,
		}
	case resp.StatusCode == http.StatusTooManyRequests:
		return s.rateLimitedResult(target, rdapURL, resp.StatusCode, "rate_limited", "RDAP rate limited", resp.Header, now)
	case resp.StatusCode >= 500:
		result := unknownResult(target, rdapURL, resp.StatusCode, fmt.Sprintf("server error HTTP %d", resp.StatusCode), now)
		result.Retryable = true
		return result
	case resp.StatusCode != http.StatusOK:
		return unknownResult(target, rdapURL, resp.StatusCode, fmt.Sprintf("HTTP %d", resp.StatusCode), now)
	}

	if len(body) == httpkit.MaxBodyBytes {
		return unknownResult(target, rdapURL, resp.StatusCode, "response too large", now)
	}
	rawWriteError := ""
	if s.settings.RDAP.RawOutputDir != "" {
		if err := writeRawRDAP(s.settings.RDAP.RawOutputDir, target.Domain, body); err != nil {
			rawWriteError = "write raw RDAP: " + err.Error()
		}
	}

	var data rdapDomainResponse
	if err := json.Unmarshal(body, &data); err != nil {
		return unknownResult(target, rdapURL, resp.StatusCode, "invalid JSON: "+err.Error(), now)
	}
	if strings.ToLower(data.ObjectClassName) != "domain" {
		return unknownResult(target, rdapURL, resp.StatusCode, "RDAP object is not a domain", now)
	}

	events := parseRDAPEvents(data.Events)
	availability := types.ClassifyAvailability(data.Status)
	lifecycle, expiringSoon, expiresInDays := types.DetermineLifecycle(availability, data.Status, events["expires_at"], s.settings.ExpiringSoonDays, now)

	return types.Result{
		Domain:        target.Domain,
		Input:         target.Input,
		LineNumber:    target.LineNumber,
		Availability:  availability,
		Lifecycle:     lifecycle,
		Confidence:    "authoritative",
		CreatedAt:     events["created_at"],
		ExpiresAt:     events["expires_at"],
		UpdatedAt:     events["updated_at"],
		Statuses:      data.Status,
		Registrar:     findRegistrar(data.Entities),
		Source:        rdapURL,
		HTTPStatus:    resp.StatusCode,
		Error:         rawWriteError,
		CheckedAt:     now,
		ExpiringSoon:  expiringSoon,
		ExpiresInDays: expiresInDays,
	}
}

func (s *RDAPSource) rateLimitedResult(target types.Target, source string, httpStatus int, lifecycle string, message string, header http.Header, now time.Time) types.Result {
	retryAfter, retryAfterSet := httpkit.RetryAfterFromHeader(header, now, s.settings.RateLimit)
	retryAfter = httpkit.ClampRetryAfter(retryAfter, s.settings.SourceMaxDelay)
	if retryAfterSet {
		message += "; retry_after=" + retryAfter.String()
	}
	return types.Result{
		Domain:        target.Domain,
		Input:         target.Input,
		LineNumber:    target.LineNumber,
		Availability:  types.AvailabilityRateLimited,
		Lifecycle:     lifecycle,
		Confidence:    "unknown",
		Source:        source,
		HTTPStatus:    httpStatus,
		Error:         message,
		CheckedAt:     now,
		RetryAfter:    retryAfter,
		RetryAfterSet: true,
	}
}

func (s *RDAPSource) baseFor(domain string) string {
	if s.bootstrap == nil {
		return ""
	}
	return s.bootstrap.FindBase(domain)
}

func (s *RDAPSource) retryConfig() httpkit.RetryConfig {
	return httpkit.RetryConfig{
		Retries: s.settings.NetworkRetries,
		Delay:   s.settings.NetworkRetryDelay,
	}
}

// FindBase locates the RDAP base URL for a domain using longest-suffix match.
func (b *RDAPBootstrap) FindBase(domain string) string {
	labels := strings.Split(strings.ToLower(domain), ".")
	var candidates []string
	for i := 0; i < len(labels); i++ {
		candidates = append(candidates, strings.Join(labels[i:], "."))
	}
	sort.SliceStable(candidates, func(i, j int) bool {
		return len(candidates[i]) > len(candidates[j])
	})

	for _, candidate := range candidates {
		for _, service := range b.Services {
			if len(service) < 2 {
				continue
			}
			tlds := service[0]
			urls := service[1]
			for _, tld := range tlds {
				if strings.EqualFold(candidate, tld) && len(urls) > 0 {
					return urls[0]
				}
			}
		}
	}
	return ""
}

func parseBootstrap(body []byte) (*RDAPBootstrap, error) {
	var bootstrap RDAPBootstrap
	decoder := json.NewDecoder(bytes.NewReader(body))
	if err := decoder.Decode(&bootstrap); err != nil {
		return nil, fmt.Errorf("parse RDAP bootstrap: %w", err)
	}
	if len(bootstrap.Services) == 0 {
		return nil, errors.New("RDAP bootstrap has no services")
	}
	return &bootstrap, nil
}

func readFreshBootstrap(path string, maxAge time.Duration) (*RDAPBootstrap, bool) {
	info, err := os.Stat(path)
	if err != nil || time.Since(info.ModTime()) > maxAge {
		return nil, false
	}
	return readAnyBootstrap(path)
}

func readAnyBootstrap(path string) (*RDAPBootstrap, bool) {
	if path == "" {
		return nil, false
	}
	body, err := os.ReadFile(path)
	if err != nil {
		return nil, false
	}
	bootstrap, err := parseBootstrap(body)
	return bootstrap, err == nil
}

func joinRDAPURL(base string, domain string) string {
	if !strings.HasSuffix(base, "/") {
		base += "/"
	}
	return base + "domain/" + url.PathEscape(domain)
}

func unknownResult(target types.Target, source string, httpStatus int, message string, now time.Time) types.Result {
	return types.Result{
		Domain:       target.Domain,
		Input:        target.Input,
		LineNumber:   target.LineNumber,
		Availability: types.AvailabilityUnknown,
		Lifecycle:    "unknown",
		Confidence:   "unknown",
		Source:       source,
		HTTPStatus:   httpStatus,
		Error:        message,
		CheckedAt:    now,
	}
}

func headerFlagEnabled(header http.Header, name string) bool {
	value := strings.TrimSpace(header.Get(name))
	if value == "" {
		return false
	}
	switch strings.ToLower(value) {
	case "0", "false", "no":
		return false
	default:
		return true
	}
}

func parseRDAPEvents(events []rdapEvent) map[string]string {
	out := map[string]string{
		"created_at": "",
		"expires_at": "",
		"updated_at": "",
	}
	for _, event := range events {
		action := strings.ToLower(strings.TrimSpace(event.EventAction))
		if event.EventDate == "" {
			continue
		}
		switch action {
		case "registration":
			out["created_at"] = event.EventDate
		case "expiration":
			out["expires_at"] = event.EventDate
		case "last changed":
			out["updated_at"] = event.EventDate
		}
	}
	return out
}

func findRegistrar(entities []rdapEntity) string {
	for _, entity := range entities {
		for _, role := range entity.Roles {
			if strings.EqualFold(role, "registrar") {
				if name := vcardFN(entity.VCardArray); name != "" {
					if entity.Handle != "" {
						return name + " (" + entity.Handle + ")"
					}
					return name
				}
				return entity.Handle
			}
		}
	}
	return ""
}

func writeRawRDAP(dir string, domain string, body []byte) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	filename := safeFilename(domain) + ".json"
	return os.WriteFile(filepath.Join(dir, filename), body, 0o644)
}

func safeFilename(value string) string {
	return strings.Map(func(r rune) rune {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '.' || r == '-' || r == '_' {
			return r
		}
		return '_'
	}, value)
}

func vcardFN(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var outer []any
	if err := json.Unmarshal(raw, &outer); err != nil || len(outer) < 2 {
		return ""
	}
	props, ok := outer[1].([]any)
	if !ok {
		return ""
	}
	for _, prop := range props {
		items, ok := prop.([]any)
		if !ok || len(items) < 4 {
			continue
		}
		name, _ := items[0].(string)
		if strings.EqualFold(name, "fn") {
			value, _ := items[3].(string)
			return value
		}
	}
	return ""
}
