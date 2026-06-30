package qda

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestSplitDomainForHostingerAvailability(t *testing.T) {
	tests := []struct {
		domain string
		name   string
		tld    string
	}{
		{domain: "example.com", name: "example", tld: "com"},
		{domain: "example.com.br", name: "example", tld: "com.br"},
	}

	for _, tt := range tests {
		name, tld, err := splitDomainForAvailability(tt.domain)
		if err != nil {
			t.Fatal(err)
		}
		if name != tt.name || tld != tt.tld {
			t.Fatalf("%s split to %s/%s, want %s/%s", tt.domain, name, tld, tt.name, tt.tld)
		}
	}
}

func TestHostingerSourceCheck(t *testing.T) {
	var receivedAuth string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedAuth = r.Header.Get("Authorization")
		if r.URL.Path != "/api/domains/v1/availability" {
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
		var body hostingerAvailabilityRequest
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if body.Domain != "free" || len(body.TLDs) != 1 || body.TLDs[0] != "com" {
			t.Fatalf("unexpected body: %#v", body)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[
			{
				"domain": "free.com",
				"is_available": true,
				"is_alternative": false,
				"restriction": null
			}
		]`))
	}))
	defer server.Close()

	settings := DefaultSettings()
	settings.HostingerAPIToken = "token"
	settings.HostingerAPIBaseURL = server.URL
	source, err := NewHostingerSource(settings)
	if err != nil {
		t.Fatal(err)
	}

	result := source.Check(t.Context(), Target{Domain: "free.com"})
	if receivedAuth != "Bearer token" {
		t.Fatalf("got auth %q", receivedAuth)
	}
	if result.Availability != AvailabilityAvailable || result.Registrable == nil || !*result.Registrable {
		t.Fatalf("unexpected result: %#v", result)
	}
}

func TestHostingerSourceRotatesAccountsAfterRateLimit(t *testing.T) {
	var auths []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/domains/v1/availability" {
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
		auths = append(auths, r.Header.Get("Authorization"))
		if len(auths) == 1 {
			w.Header().Set("Retry-After", "60")
			http.Error(w, "slow down", http.StatusTooManyRequests)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[
			{
				"domain": "free.com",
				"is_available": true,
				"is_alternative": false,
				"restriction": null
			}
		]`))
	}))
	defer server.Close()

	settings := DefaultSettings()
	settings.HostingerAPIToken = "primary-token"
	settings.HostingerAPIBaseURL = server.URL
	settings.HostingerRateLimit = 0
	settings.HostingerAccounts = []HostingerAccount{
		{Name: "backup", APIToken: "backup-token"},
	}
	source, err := NewHostingerSource(settings)
	if err != nil {
		t.Fatal(err)
	}

	result := source.Check(t.Context(), Target{Domain: "free.com"})
	if result.Availability != AvailabilityAvailable || result.Registrable == nil || !*result.Registrable {
		t.Fatalf("unexpected result: %#v", result)
	}
	if len(auths) != 2 {
		t.Fatalf("auths: %#v", auths)
	}
	if auths[0] != "Bearer primary-token" || auths[1] != "Bearer backup-token" {
		t.Fatalf("unexpected auth rotation: %#v", auths)
	}
}
