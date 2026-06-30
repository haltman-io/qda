package qda

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestVercelSourceCheckBulkAvailabilityAndPrice(t *testing.T) {
	var receivedAuth string
	priceCalls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedAuth = r.Header.Get("Authorization")
		switch r.URL.Path {
		case "/v1/registrar/domains/availability":
			if r.URL.Query().Get("teamId") != "team_123" {
				t.Fatalf("missing teamId query: %s", r.URL.RawQuery)
			}
			var body vercelAvailabilityRequest
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatal(err)
			}
			if len(body.Domains) != 2 || body.Domains[0] != "free.com" || body.Domains[1] != "taken.com" {
				t.Fatalf("unexpected body: %#v", body)
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{
				"results": [
					{"domain": "free.com", "available": true},
					{"domain": "taken.com", "available": false}
				]
			}`))
		case "/v1/registrar/domains/free.com/price":
			priceCalls++
			if r.URL.Query().Get("teamId") != "team_123" || r.URL.Query().Get("years") != "2" {
				t.Fatalf("unexpected price query: %s", r.URL.RawQuery)
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{
				"years": 2,
				"purchasePrice": "12.34",
				"renewalPrice": 13.5,
				"transferPrice": "9.99"
			}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	settings := DefaultSettings()
	settings.VercelAPIToken = "token"
	settings.VercelAPIBaseURL = server.URL
	settings.VercelTeamID = "team_123"
	settings.VercelRateLimit = 0
	settings.VercelFetchPrice = true
	settings.VercelPriceYears = "2"
	source, err := NewVercelSource(settings)
	if err != nil {
		t.Fatal(err)
	}

	results := source.Check(t.Context(), []Target{
		{Domain: "free.com"},
		{Domain: "taken.com"},
	})

	if receivedAuth != "Bearer token" {
		t.Fatalf("got auth %q", receivedAuth)
	}
	free := results["free.com"]
	if free.Availability != AvailabilityAvailable || free.Registrable == nil || !*free.Registrable {
		t.Fatalf("unexpected free result: %#v", free)
	}
	if free.Pricing == nil || free.Pricing.RegistrationCost != "12.34" || free.Pricing.RenewalCost != "13.5" || free.Pricing.TransferCost != "9.99" || free.Pricing.Years != "2" {
		t.Fatalf("unexpected pricing: %#v", free.Pricing)
	}
	taken := results["taken.com"]
	if taken.Availability != AvailabilityReserved || taken.Registrable == nil || *taken.Registrable {
		t.Fatalf("unexpected taken result: %#v", taken)
	}
	if priceCalls != 1 {
		t.Fatalf("price calls: %d", priceCalls)
	}
}

func TestVercelSourceRateLimited(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "slow down", http.StatusTooManyRequests)
	}))
	defer server.Close()

	settings := DefaultSettings()
	settings.VercelAPIToken = "token"
	settings.VercelAPIBaseURL = server.URL
	source, err := NewVercelSource(settings)
	if err != nil {
		t.Fatal(err)
	}

	results := source.Check(t.Context(), []Target{{Domain: "free.com"}})
	result := results["free.com"]
	if result.Availability != AvailabilityRateLimited || result.Lifecycle != "rate_limited" {
		t.Fatalf("unexpected result: %#v", result)
	}
}

func TestVercelSourceRotatesAccountsAfterRateLimit(t *testing.T) {
	var auths []string
	var teamIDs []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/registrar/domains/availability" {
			http.NotFound(w, r)
			return
		}
		auths = append(auths, r.Header.Get("Authorization"))
		teamIDs = append(teamIDs, r.URL.Query().Get("teamId"))
		if len(auths) == 1 {
			w.Header().Set("Retry-After", "60")
			http.Error(w, "slow down", http.StatusTooManyRequests)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"results": [
				{"domain": "free.com", "available": true}
			]
		}`))
	}))
	defer server.Close()

	settings := DefaultSettings()
	settings.VercelAPIToken = "primary-token"
	settings.VercelTeamID = "team_primary"
	settings.VercelAPIBaseURL = server.URL
	settings.VercelRateLimit = 0
	settings.VercelAccounts = []VercelAccount{
		{Name: "backup", APIToken: "backup-token", TeamID: "team_backup"},
	}
	source, err := NewVercelSource(settings)
	if err != nil {
		t.Fatal(err)
	}

	results := source.Check(t.Context(), []Target{{Domain: "free.com"}})
	result := results["free.com"]
	if result.Availability != AvailabilityAvailable || result.Registrable == nil || !*result.Registrable {
		t.Fatalf("unexpected result: %#v", result)
	}
	if len(auths) != 2 {
		t.Fatalf("auths: %#v", auths)
	}
	if auths[0] != "Bearer primary-token" || auths[1] != "Bearer backup-token" {
		t.Fatalf("unexpected auth rotation: %#v", auths)
	}
	if teamIDs[0] != "team_primary" || teamIDs[1] != "team_backup" {
		t.Fatalf("unexpected team rotation: %#v", teamIDs)
	}
}
