package qda

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

type ResultCache struct {
	path    string
	entries map[string]Result
	warning string
}

type cacheFile struct {
	Version int               `json:"version"`
	Entries map[string]Result `json:"entries"`
}

func OpenResultCache(settings Settings) (*ResultCache, error) {
	if !settings.CacheEnabled {
		return &ResultCache{entries: map[string]Result{}}, nil
	}

	path, err := resultCachePath(settings)
	if err != nil {
		return nil, err
	}

	cache := &ResultCache{
		path:    path,
		entries: map[string]Result{},
	}

	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return cache, nil
		}
		return nil, fmt.Errorf("read result cache: %w", err)
	}

	var file cacheFile
	if err := json.Unmarshal(data, &file); err != nil {
		cache.warning = "ignoring invalid result cache: " + err.Error()
		return cache, nil
	}
	if file.Entries != nil {
		cache.entries = file.Entries
	}

	return cache, nil
}

func (c *ResultCache) Warning() string {
	if c == nil {
		return ""
	}
	return c.warning
}

func (c *ResultCache) ReusableRegistered(target Target, now time.Time, expiringSoonDays int) (Result, bool) {
	return c.ReusableResult(target, now, expiringSoonDays)
}

func (c *ResultCache) ReusableResult(target Target, now time.Time, expiringSoonDays int) (Result, bool) {
	if c == nil {
		return Result{}, false
	}
	result, ok := c.entries[target.Domain]
	if !ok || !isReusableCachedResult(result, now) {
		return Result{}, false
	}

	result.Input = target.Input
	result.LineNumber = target.LineNumber
	result.CacheHit = true
	result.CacheReason = cacheHitReason(result)
	result.Lifecycle, result.ExpiringSoon, result.ExpiresInDays = DetermineLifecycle(result.Availability, result.Statuses, result.ExpiresAt, expiringSoonDays, now)
	return result, true
}

func (c *ResultCache) Store(result Result) {
	if c == nil || c.entries == nil {
		return
	}
	if isTransientCacheResult(result) {
		if existing, ok := c.entries[result.Domain]; ok && !isTransientCacheResult(existing) {
			return
		}
	}
	result.CacheHit = false
	result.CacheReason = ""
	c.entries[result.Domain] = result
}

func (c *ResultCache) Save() error {
	if c == nil || c.path == "" {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(c.path), 0o755); err != nil {
		return fmt.Errorf("create result cache directory: %w", err)
	}
	data, err := json.MarshalIndent(cacheFile{
		Version: 1,
		Entries: c.entries,
	}, "", "  ")
	if err != nil {
		return fmt.Errorf("encode result cache: %w", err)
	}
	if err := os.WriteFile(c.path, data, 0o644); err != nil {
		return fmt.Errorf("write result cache: %w", err)
	}
	return nil
}

func resultCachePath(settings Settings) (string, error) {
	if settings.CachePath != "" {
		return settings.CachePath, nil
	}
	cacheDir, err := os.UserCacheDir()
	if err != nil {
		return "", fmt.Errorf("resolve user cache directory: %w", err)
	}
	return filepath.Join(cacheDir, "qda", "results.json"), nil
}

func isReusableRegistered(result Result, now time.Time) bool {
	if result.Availability != AvailabilityRegistered {
		return false
	}
	if result.ExpiresAt == "" {
		return true
	}
	expiresAt, err := time.Parse(time.RFC3339, result.ExpiresAt)
	if err != nil {
		return false
	}
	return expiresAt.After(now)
}

func isReusableCachedResult(result Result, now time.Time) bool {
	if IsAvailableLike(result) || IsAvailableSoon(result) {
		return false
	}
	switch result.Availability {
	case AvailabilityRegistered:
		return isReusableRegistered(result, now)
	case AvailabilityReserved:
		return true
	default:
		return false
	}
}

func isTransientCacheResult(result Result) bool {
	switch result.Availability {
	case AvailabilityUnknown, AvailabilityRateLimited, AvailabilityInvalid:
		return true
	default:
		return false
	}
}

func cacheHitReason(result Result) string {
	switch result.Availability {
	case AvailabilityRegistered:
		return "registered domain cache hit"
	case AvailabilityReserved:
		return "non-registrable domain cache hit"
	default:
		return "cache hit"
	}
}
