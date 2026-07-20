// Package httpkit provides the shared HTTP plumbing: clients with proxy
// rotation, transient network retries and Retry-After parsing.
package httpkit

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
)

// MaxBodyBytes caps every upstream response body.
const MaxBodyBytes = 4 * 1024 * 1024

// RetryConfig controls transient network retries.
type RetryConfig struct {
	Retries int
	Delay   time.Duration
	Timeout time.Duration
}

// NewClient builds an HTTP client that rotates through the given proxies.
func NewClient(timeout time.Duration, proxies []string, proxyFile string) (*http.Client, error) {
	transport := &http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		MaxIdleConns:          128,
		MaxIdleConnsPerHost:   16,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ResponseHeaderTimeout: timeout,
	}

	proxyURLs, err := LoadProxyURLs(proxies, proxyFile)
	if err != nil {
		return nil, err
	}
	if len(proxyURLs) > 0 {
		var index uint64
		transport.Proxy = func(req *http.Request) (*url.URL, error) {
			next := atomic.AddUint64(&index, 1)
			return proxyURLs[int(next-1)%len(proxyURLs)], nil
		}
	}

	return &http.Client{
		Timeout:   timeout,
		Transport: transport,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}, nil
}

// LoadProxyURLs parses inline proxies plus an optional file with one
// proxy URL per line (for example the proton-privoxy endpoint
// http://localhost:8100).
func LoadProxyURLs(values []string, proxyFile string) ([]*url.URL, error) {
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

// Do executes buildRequest with transient-network-error retries and reads
// the response body (bounded by MaxBodyBytes).
func Do(ctx context.Context, client *http.Client, cfg RetryConfig, buildRequest func() (*http.Request, error)) (*http.Response, []byte, error) {
	attempts := cfg.Retries + 1
	if attempts < 1 {
		attempts = 1
	}

	var lastErr error
	for attempt := 0; attempt < attempts; attempt++ {
		resp, body, err := doOnce(client, buildRequest)
		if err == nil {
			return resp, body, nil
		}
		lastErr = err
		if attempt == attempts-1 || !shouldRetryNetworkError(ctx, err) {
			return nil, nil, RetryError{Attempts: attempt + 1, Err: err}
		}
		if err := SleepContext(ctx, cfg.Delay); err != nil {
			return nil, nil, err
		}
	}

	return nil, nil, lastErr
}

func doOnce(client *http.Client, buildRequest func() (*http.Request, error)) (*http.Response, []byte, error) {
	req, err := buildRequest()
	if err != nil {
		return nil, nil, BuildError{Err: err}
	}
	resp, err := client.Do(req)
	if err != nil {
		if resp != nil && resp.Body != nil {
			_ = resp.Body.Close()
		}
		return nil, nil, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, MaxBodyBytes))
	if err != nil {
		return nil, nil, err
	}
	return resp, body, nil
}

func shouldRetryNetworkError(ctx context.Context, err error) bool {
	if err == nil {
		return false
	}
	var buildErr BuildError
	if errors.As(err, &buildErr) {
		return false
	}
	if ctx != nil && ctx.Err() != nil {
		return false
	}
	return true
}

// FailureMessage renders a friendly error including retry attempts.
func FailureMessage(err error) string {
	var retryErr RetryError
	if errors.As(err, &retryErr) && retryErr.Attempts > 1 {
		return "request failed after " + strconv.Itoa(retryErr.Attempts) + " attempts: " + retryErr.Err.Error()
	}
	return "request failed: " + err.Error()
}

// BuildError marks request construction failures (never retried).
type BuildError struct {
	Err error
}

func (e BuildError) Error() string { return e.Err.Error() }
func (e BuildError) Unwrap() error { return e.Err }

// RetryError reports how many attempts were made before giving up.
type RetryError struct {
	Attempts int
	Err      error
}

func (e RetryError) Error() string { return e.Err.Error() }
func (e RetryError) Unwrap() error { return e.Err }

// SleepContext sleeps unless the context is cancelled first.
func SleepContext(ctx context.Context, delay time.Duration) error {
	if delay <= 0 {
		return nil
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-timer.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// RetryAfterFromHeader parses the Retry-After header (seconds or HTTP date).
// The boolean reports whether the header was present and parseable.
func RetryAfterFromHeader(header http.Header, now time.Time, fallback time.Duration) (time.Duration, bool) {
	value := strings.TrimSpace(header.Get("Retry-After"))
	if value == "" {
		if fallback < 0 {
			return 0, false
		}
		return fallback, false
	}
	if seconds, err := strconv.Atoi(value); err == nil {
		if seconds < 0 {
			return 0, true
		}
		return time.Duration(seconds) * time.Second, true
	}
	if retryAt, err := http.ParseTime(value); err == nil {
		if retryAt.Before(now) {
			return 0, true
		}
		return retryAt.Sub(now), true
	}
	if fallback < 0 {
		return 0, true
	}
	return fallback, true
}

// ClampRetryAfter caps a delay when maxDelay is positive.
func ClampRetryAfter(retryAfter time.Duration, maxDelay time.Duration) time.Duration {
	if retryAfter < 0 {
		return 0
	}
	if maxDelay > 0 && retryAfter > maxDelay {
		return maxDelay
	}
	return retryAfter
}
