package qda

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestReusableRegisteredCacheSkipsFutureExpiration(t *testing.T) {
	cache := &ResultCache{
		entries: map[string]Result{
			"example.com": {
				Domain:       "example.com",
				Availability: AvailabilityRegistered,
				ExpiresAt:    "2030-01-01T00:00:00Z",
			},
		},
	}

	result, ok := cache.ReusableRegistered(Target{Domain: "example.com", Input: "example", LineNumber: 7}, time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), 30)
	if !ok {
		t.Fatal("expected cache hit")
	}
	if !result.CacheHit || result.LineNumber != 7 {
		t.Fatalf("unexpected cached result: %#v", result)
	}
}

func TestReusableRegisteredCacheRechecksExpired(t *testing.T) {
	cache := &ResultCache{
		entries: map[string]Result{
			"example.com": {
				Domain:       "example.com",
				Availability: AvailabilityRegistered,
				ExpiresAt:    "2020-01-01T00:00:00Z",
			},
		},
	}

	if _, ok := cache.ReusableRegistered(Target{Domain: "example.com"}, time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), 30); ok {
		t.Fatal("expired registered domain must be rechecked")
	}
}

func TestReusableRegisteredCacheDoesNotSkipAvailable(t *testing.T) {
	cache := &ResultCache{
		entries: map[string]Result{
			"example.com": {
				Domain:       "example.com",
				Availability: AvailabilityAvailable,
			},
		},
	}

	if _, ok := cache.ReusableRegistered(Target{Domain: "example.com"}, time.Now().UTC(), 30); ok {
		t.Fatal("available domains must be checked again")
	}
}

func TestReusableResultCacheSkipsReserved(t *testing.T) {
	cache := &ResultCache{
		entries: map[string]Result{
			"example.com": {
				Domain:       "example.com",
				Availability: AvailabilityReserved,
				Lifecycle:    "not_registrable",
				Confidence:   "cloudflare_authoritative",
			},
		},
	}

	result, ok := cache.ReusableResult(Target{Domain: "example.com", Input: "example", LineNumber: 9}, time.Now().UTC(), 30)
	if !ok {
		t.Fatal("expected reserved cache hit")
	}
	if !result.CacheHit || result.CacheReason != "non-registrable domain cache hit" || result.LineNumber != 9 {
		t.Fatalf("unexpected cached result: %#v", result)
	}
}

func TestCacheStoreDoesNotOverwriteStableResultWithTransientFailure(t *testing.T) {
	cache := &ResultCache{
		entries: map[string]Result{
			"example.com": {
				Domain:       "example.com",
				Availability: AvailabilityReserved,
				Lifecycle:    "not_registrable",
			},
		},
	}

	cache.Store(Result{
		Domain:       "example.com",
		Availability: AvailabilityRateLimited,
		Lifecycle:    "rate_limited",
		Error:        "rate limited",
	})

	if cache.entries["example.com"].Availability != AvailabilityReserved {
		t.Fatalf("transient result overwrote stable cache: %#v", cache.entries["example.com"])
	}
}

func TestReusableRegisteredCacheRecomputesExpiringSoon(t *testing.T) {
	cache := &ResultCache{
		entries: map[string]Result{
			"example.com": {
				Domain:       "example.com",
				Availability: AvailabilityRegistered,
				Lifecycle:    "active",
				ExpiresAt:    "2026-01-20T00:00:00Z",
			},
		},
	}

	result, ok := cache.ReusableRegistered(Target{Domain: "example.com"}, time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), 60)
	if !ok {
		t.Fatal("expected cache hit")
	}
	if result.Lifecycle != "expiring_soon" || !result.ExpiringSoon {
		t.Fatalf("expected expiring soon cache result, got %#v", result)
	}
}

func TestOpenResultCacheIgnoresInvalidJSON(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cache.json")
	if err := os.WriteFile(path, []byte("{bad json"), 0o644); err != nil {
		t.Fatal(err)
	}

	settings := DefaultSettings()
	settings.CachePath = path
	cache, err := OpenResultCache(settings)
	if err != nil {
		t.Fatal(err)
	}
	if cache.Warning() == "" {
		t.Fatal("expected warning")
	}
}
