package qda

import (
	"net/http"
	"strconv"
	"strings"
	"time"
)

type sourceBackoff struct {
	until time.Time
}

func (b *sourceBackoff) Active(now time.Time) bool {
	return !b.until.IsZero() && now.Before(b.until)
}

func (b *sourceBackoff) Delay(now time.Time) time.Duration {
	if !b.Active(now) {
		return 0
	}
	return b.until.Sub(now)
}

func (b *sourceBackoff) Postpone(now time.Time, retryAfter time.Duration) {
	if retryAfter < 0 {
		retryAfter = 0
	}
	until := now.Add(retryAfter)
	if until.After(b.until) {
		b.until = until
	}
}

func retryAfterFromHeader(header http.Header, now time.Time, fallback time.Duration) time.Duration {
	value := strings.TrimSpace(header.Get("Retry-After"))
	if value == "" {
		if fallback < 0 {
			return 0
		}
		return fallback
	}
	if seconds, err := strconv.Atoi(value); err == nil {
		if seconds < 0 {
			return 0
		}
		return time.Duration(seconds) * time.Second
	}
	if retryAt, err := http.ParseTime(value); err == nil {
		if retryAt.Before(now) {
			return 0
		}
		return retryAt.Sub(now)
	}
	if fallback < 0 {
		return 0
	}
	return fallback
}

func clampRetryAfter(retryAfter time.Duration, maxDelay time.Duration) time.Duration {
	if retryAfter < 0 {
		return 0
	}
	if maxDelay > 0 && retryAfter > maxDelay {
		return maxDelay
	}
	return retryAfter
}

func sourceResultRetryAfter(results map[string]SourceResult, fallback time.Duration) (time.Duration, bool) {
	if len(results) == 0 {
		return 0, false
	}
	var retryAfter time.Duration
	found := false
	for _, result := range results {
		if result.Availability != AvailabilityRateLimited {
			return 0, false
		}
		candidate := fallback
		if result.RetryAfterSet {
			candidate = result.RetryAfter
		}
		if !found || candidate > retryAfter {
			retryAfter = candidate
			found = true
		}
	}
	if !found {
		retryAfter = fallback
	}
	if retryAfter < 0 {
		retryAfter = 0
	}
	return retryAfter, true
}

func sourceResultIsRateLimited(result SourceResult, fallback time.Duration) (time.Duration, bool) {
	if result.Availability != AvailabilityRateLimited {
		return 0, false
	}
	retryAfter := fallback
	if result.RetryAfterSet {
		retryAfter = result.RetryAfter
	}
	if retryAfter < 0 {
		retryAfter = 0
	}
	return retryAfter, true
}
