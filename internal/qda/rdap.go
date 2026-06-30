package qda

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync/atomic"
	"time"
)

const maxRDAPBodyBytes = 4 * 1024 * 1024

type RDAPBootstrap struct {
	Services [][][]string `json:"services"`
}

type BootstrapLoad struct {
	Bootstrap *RDAPBootstrap
	Source    string
	Warning   string
}

type RDAPClient struct {
	httpClient *http.Client
	settings   Settings
}

type rdapDomainResponse struct {
	ObjectClassName string          `json:"objectClassName"`
	LDHName         string          `json:"ldhName"`
	UnicodeName     string          `json:"unicodeName"`
	Status          []string        `json:"status"`
	Events          []rdapEvent     `json:"events"`
	Entities        []rdapEntity    `json:"entities"`
	Notices         []rdapNotice    `json:"notices"`
	Remarks         []rdapNotice    `json:"remarks"`
	ErrorCode       int             `json:"errorCode"`
	Title           string          `json:"title"`
	Description     []string        `json:"description"`
	Raw             json.RawMessage `json:"-"`
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

type rdapNotice struct {
	Title       string   `json:"title"`
	Description []string `json:"description"`
}

func NewRDAPClient(settings Settings) (*RDAPClient, error) {
	httpClient, err := newHTTPClient(settings)
	if err != nil {
		return nil, err
	}
	return &RDAPClient{
		httpClient: httpClient,
		settings:   settings,
	}, nil
}

func newHTTPClient(settings Settings) (*http.Client, error) {
	transport := &http.Transport{
		Proxy: http.ProxyFromEnvironment,
	}

	proxies, err := loadProxyURLs(settings.ProxyURLs, settings.ProxyFile)
	if err != nil {
		return nil, err
	}
	if len(proxies) > 0 {
		var index uint64
		transport.Proxy = func(req *http.Request) (*url.URL, error) {
			next := atomic.AddUint64(&index, 1)
			return proxies[int(next-1)%len(proxies)], nil
		}
	}

	return &http.Client{
		Timeout:   settings.Timeout,
		Transport: transport,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}, nil
}

func loadProxyURLs(values []string, proxyFile string) ([]*url.URL, error) {
	var raw []string
	raw = append(raw, values...)

	if strings.TrimSpace(proxyFile) != "" {
		data, err := os.ReadFile(proxyFile)
		if err != nil {
			return nil, fmt.Errorf("read proxy file: %w", err)
		}
		for _, line := range strings.Split(string(data), "\n") {
			line = strings.TrimSpace(line)
			if line == "" || strings.HasPrefix(line, "#") {
				continue
			}
			raw = append(raw, line)
		}
	}

	out := make([]*url.URL, 0, len(raw))
	for _, value := range raw {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		parsed, err := url.Parse(value)
		if err != nil {
			return nil, fmt.Errorf("invalid proxy URL %q: %w", value, err)
		}
		if parsed.Scheme == "" || parsed.Host == "" {
			return nil, fmt.Errorf("invalid proxy URL %q: scheme and host are required", value)
		}
		out = append(out, parsed)
	}
	return out, nil
}

func (c *RDAPClient) LoadBootstrap(ctx context.Context) (BootstrapLoad, error) {
	cachePath, err := c.bootstrapCachePath()
	if err != nil {
		return BootstrapLoad{}, err
	}

	if cachePath != "" {
		if bootstrap, ok := readFreshBootstrap(cachePath, 24*time.Hour); ok {
			return BootstrapLoad{Bootstrap: bootstrap, Source: cachePath}, nil
		}
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.settings.BootstrapURL, nil)
	if err != nil {
		return BootstrapLoad{}, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", c.settings.UserAgent)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		if cachePath != "" {
			if bootstrap, ok := readAnyBootstrap(cachePath); ok {
				return BootstrapLoad{
					Bootstrap: bootstrap,
					Source:    cachePath,
					Warning:   "using stale RDAP bootstrap cache because refresh failed: " + err.Error(),
				}, nil
			}
		}
		return BootstrapLoad{}, fmt.Errorf("load RDAP bootstrap: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		if cachePath != "" {
			if bootstrap, ok := readAnyBootstrap(cachePath); ok {
				return BootstrapLoad{
					Bootstrap: bootstrap,
					Source:    cachePath,
					Warning:   fmt.Sprintf("using stale RDAP bootstrap cache because refresh returned HTTP %d", resp.StatusCode),
				}, nil
			}
		}
		return BootstrapLoad{}, fmt.Errorf("load RDAP bootstrap: HTTP %d", resp.StatusCode)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxRDAPBodyBytes))
	if err != nil {
		return BootstrapLoad{}, fmt.Errorf("read RDAP bootstrap: %w", err)
	}

	bootstrap, err := parseBootstrap(body)
	if err != nil {
		return BootstrapLoad{}, err
	}

	if cachePath != "" {
		_ = os.MkdirAll(filepath.Dir(cachePath), 0o755)
		_ = os.WriteFile(cachePath, body, 0o644)
	}

	return BootstrapLoad{Bootstrap: bootstrap, Source: c.settings.BootstrapURL}, nil
}

func (c *RDAPClient) QueryDomain(ctx context.Context, bootstrap *RDAPBootstrap, target Target) Result {
	now := time.Now().UTC()
	base := bootstrap.FindBase(target.Domain)
	if base == "" {
		return Result{
			Domain:       target.Domain,
			Input:        target.Input,
			LineNumber:   target.LineNumber,
			Availability: AvailabilityUnknown,
			Lifecycle:    "unknown",
			Confidence:   "none",
			Error:        "no RDAP bootstrap endpoint",
			CheckedAt:    now,
		}
	}

	rdapURL := joinRDAPURL(base, target.Domain)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rdapURL, nil)
	if err != nil {
		return unknownResult(target, rdapURL, 0, "build request: "+err.Error(), now)
	}
	req.Header.Set("Accept", "application/rdap+json, application/json")
	req.Header.Set("User-Agent", c.settings.UserAgent)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return unknownResult(target, rdapURL, 0, err.Error(), now)
		}
		return unknownResult(target, rdapURL, 0, "request failed: "+err.Error(), now)
	}
	defer resp.Body.Close()

	switch {
	case resp.StatusCode == http.StatusNotFound:
		return Result{
			Domain:       target.Domain,
			Input:        target.Input,
			LineNumber:   target.LineNumber,
			Availability: AvailabilityAvailable,
			Lifecycle:    "available",
			Confidence:   "authoritative",
			Source:       rdapURL,
			HTTPStatus:   resp.StatusCode,
			CheckedAt:    now,
		}
	case resp.StatusCode == http.StatusTooManyRequests:
		return Result{
			Domain:       target.Domain,
			Input:        target.Input,
			LineNumber:   target.LineNumber,
			Availability: AvailabilityRateLimited,
			Lifecycle:    "rate_limited",
			Confidence:   "unknown",
			Source:       rdapURL,
			HTTPStatus:   resp.StatusCode,
			Error:        "rate limited",
			CheckedAt:    now,
		}
	case resp.StatusCode >= 500:
		return unknownResult(target, rdapURL, resp.StatusCode, fmt.Sprintf("server error HTTP %d", resp.StatusCode), now)
	case resp.StatusCode != http.StatusOK:
		return unknownResult(target, rdapURL, resp.StatusCode, fmt.Sprintf("HTTP %d", resp.StatusCode), now)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxRDAPBodyBytes))
	if err != nil {
		return unknownResult(target, rdapURL, resp.StatusCode, "read response: "+err.Error(), now)
	}
	if len(body) == maxRDAPBodyBytes {
		return unknownResult(target, rdapURL, resp.StatusCode, "response too large", now)
	}
	rawWriteError := ""
	if c.settings.RawOutputDir != "" {
		if err := writeRawRDAP(c.settings.RawOutputDir, target.Domain, body); err != nil {
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
	availability := ClassifyAvailability(data.Status)
	lifecycle, expiringSoon, expiresInDays := DetermineLifecycle(availability, data.Status, events["expires_at"], c.settings.ExpiringSoonDays, now)

	return Result{
		Domain:        target.Domain,
		Input:         target.Input,
		LineNumber:    target.LineNumber,
		Availability:  availability,
		Lifecycle:     lifecycle,
		Confidence:    "authoritative",
		CreatedAt:     events["created_at"],
		ExpiresAt:     events["expires_at"],
		UpdatedAt:     events["updated_at"],
		RDAPUpdatedAt: events["rdap_updated_at"],
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
	body, err := os.ReadFile(path)
	if err != nil {
		return nil, false
	}
	bootstrap, err := parseBootstrap(body)
	return bootstrap, err == nil
}

func (c *RDAPClient) bootstrapCachePath() (string, error) {
	if strings.TrimSpace(c.settings.BootstrapCachePath) != "" {
		return c.settings.BootstrapCachePath, nil
	}
	cacheDir, err := os.UserCacheDir()
	if err != nil {
		return "", nil
	}
	return filepath.Join(cacheDir, "qda", "rdap-dns.json"), nil
}

func joinRDAPURL(base string, domain string) string {
	if !strings.HasSuffix(base, "/") {
		base += "/"
	}
	return base + "domain/" + url.PathEscape(domain)
}

func unknownResult(target Target, source string, httpStatus int, message string, now time.Time) Result {
	return Result{
		Domain:       target.Domain,
		Input:        target.Input,
		LineNumber:   target.LineNumber,
		Availability: AvailabilityUnknown,
		Lifecycle:    "unknown",
		Confidence:   "unknown",
		Source:       source,
		HTTPStatus:   httpStatus,
		Error:        message,
		CheckedAt:    now,
	}
}

func parseRDAPEvents(events []rdapEvent) map[string]string {
	out := map[string]string{
		"created_at":      "",
		"expires_at":      "",
		"updated_at":      "",
		"rdap_updated_at": "",
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
		case "last update of rdap database":
			out["rdap_updated_at"] = event.EventDate
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
