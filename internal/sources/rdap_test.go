package sources

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"qda/internal/config"
	"qda/internal/types"
)

// fakeRDAP spins up an httptest server that serves the IANA bootstrap and
// per-domain RDAP answers.
func fakeRDAP(t *testing.T, handler http.HandlerFunc) (config.Settings, func()) {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/domain/", handler)
	server := httptest.NewServer(mux)

	bootstrap := RDAPBootstrap{
		Services: [][][]string{
			{{"net"}, {server.URL + "/"}},
		},
	}
	bootstrapServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(bootstrap)
	}))

	settings := config.Default()
	settings.RDAP.BootstrapURL = bootstrapServer.URL
	settings.RDAP.BootstrapCachePath = ""
	settings.NetworkRetries = 0
	settings.RateLimit = 0

	return settings, func() {
		server.Close()
		bootstrapServer.Close()
	}
}

func loadSource(t *testing.T, settings config.Settings) *RDAPSource {
	t.Helper()
	source, err := NewRDAP(settings)
	if err != nil {
		t.Fatal(err)
	}
	if err := source.Load(context.Background()); err != nil {
		t.Fatal(err)
	}
	return source
}

func TestRDAPAvailableOn404(t *testing.T) {
	settings, cleanup := fakeRDAP(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	})
	defer cleanup()

	source := loadSource(t, settings)
	result := source.Check(context.Background(), types.Target{Domain: "free.net"})
	if result.Availability != types.AvailabilityAvailable {
		t.Fatalf("availability = %s, want available", result.Availability)
	}
	if result.Lifecycle != "available" {
		t.Fatalf("lifecycle = %s", result.Lifecycle)
	}
}

func TestRDAPRegisteredWithEvents(t *testing.T) {
	settings, cleanup := fakeRDAP(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/rdap+json")
		fmt.Fprint(w, `{
			"objectClassName": "domain",
			"ldhName": "taken.net",
			"status": ["active"],
			"events": [
				{"eventAction": "registration", "eventDate": "2020-01-01T00:00:00Z"},
				{"eventAction": "expiration", "eventDate": "2030-01-01T00:00:00Z"}
			]
		}`)
	})
	defer cleanup()

	source := loadSource(t, settings)
	result := source.Check(context.Background(), types.Target{Domain: "taken.net"})
	if result.Availability != types.AvailabilityRegistered {
		t.Fatalf("availability = %s", result.Availability)
	}
	if result.ExpiresAt != "2030-01-01T00:00:00Z" {
		t.Fatalf("expires = %q", result.ExpiresAt)
	}
	if result.Lifecycle != "active" {
		t.Fatalf("lifecycle = %q", result.Lifecycle)
	}
}

func TestRDAPRedemptionClassification(t *testing.T) {
	settings, cleanup := fakeRDAP(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/rdap+json")
		fmt.Fprint(w, `{
			"objectClassName": "domain",
			"ldhName": "drop.net",
			"status": ["redemption period"]
		}`)
	})
	defer cleanup()

	source := loadSource(t, settings)
	result := source.Check(context.Background(), types.Target{Domain: "drop.net"})
	if result.Availability != types.AvailabilityRedemption {
		t.Fatalf("availability = %s, want redemption", result.Availability)
	}
}

func TestRDAPRateLimitParsesRetryAfter(t *testing.T) {
	settings, cleanup := fakeRDAP(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "7")
		w.WriteHeader(http.StatusTooManyRequests)
	})
	defer cleanup()

	source := loadSource(t, settings)
	result := source.Check(context.Background(), types.Target{Domain: "limited.net"})
	if result.Availability != types.AvailabilityRateLimited {
		t.Fatalf("availability = %s", result.Availability)
	}
	if !result.RetryAfterSet || result.RetryAfter != 7*time.Second {
		t.Fatalf("retry after = %s (set=%v)", result.RetryAfter, result.RetryAfterSet)
	}
}

func TestRDAPNicBrPermissionDenied(t *testing.T) {
	settings, cleanup := fakeRDAP(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Nicbr-Permission-Denied", "1")
		w.WriteHeader(http.StatusForbidden)
	})
	defer cleanup()

	source := loadSource(t, settings)
	result := source.Check(context.Background(), types.Target{Domain: "x.net"})
	if result.Availability != types.AvailabilityRateLimited || result.Lifecycle != "permission_denied" {
		t.Fatalf("result = %s/%s", result.Availability, result.Lifecycle)
	}
}

func TestRDAPKeyForUsesRegistryHost(t *testing.T) {
	settings, cleanup := fakeRDAP(t, func(w http.ResponseWriter, r *http.Request) {})
	defer cleanup()

	source := loadSource(t, settings)
	key := source.KeyFor("anything.net")
	if key == "rdap" || key == "" {
		t.Fatalf("unexpected key %q", key)
	}
}
