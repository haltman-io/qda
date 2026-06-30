package qda

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestCloudflareSourceCheck(t *testing.T) {
	var receivedAuth string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedAuth = r.Header.Get("Authorization")
		if r.URL.Path != "/accounts/account/registrar/domain-check" {
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
		var body cloudflareCheckRequest
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if len(body.Domains) != 2 {
			t.Fatalf("got domains %#v", body.Domains)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"success": true,
			"errors": [],
			"messages": [],
			"result": {
				"domains": [
					{
						"name": "free.com",
						"registrable": true,
						"tier": "standard",
						"pricing": {
							"currency": "USD",
							"registration_cost": "8.57",
							"renewal_cost": "8.57"
						}
					},
					{
						"name": "taken.com",
						"registrable": false,
						"reason": "domain_unavailable"
					}
				]
			}
		}`))
	}))
	defer server.Close()

	settings := DefaultSettings()
	settings.CloudflareAccountID = "account"
	settings.CloudflareAPIToken = "token"
	settings.CloudflareAPIBaseURL = server.URL
	settings.CloudflareBatchSize = 20

	source, err := NewCloudflareSource(settings)
	if err != nil {
		t.Fatal(err)
	}
	results := source.Check(t.Context(), []Target{{Domain: "free.com"}, {Domain: "taken.com"}})

	if receivedAuth != "Bearer token" {
		t.Fatalf("got auth %q", receivedAuth)
	}
	if got := results["free.com"]; got.Registrable == nil || !*got.Registrable || got.Pricing == nil {
		t.Fatalf("unexpected free result: %#v", got)
	}
	if got := results["taken.com"]; got.Reason != "domain_unavailable" {
		t.Fatalf("unexpected taken result: %#v", got)
	}
}
