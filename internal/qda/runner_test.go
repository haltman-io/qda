package qda

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"
)

func TestRunChecksStreamsEachBatch(t *testing.T) {
	releaseSecond := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/dns.json":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"services":[[["test"],["` + "http://" + r.Host + `/rdap/"]]]}`))
		case "/rdap/domain/one.test", "/rdap/domain/two.test":
			http.NotFound(w, r)
		case "/accounts/account/registrar/domain-check":
			var request cloudflareCheckRequest
			if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
				t.Error(err)
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			if len(request.Domains) == 1 && request.Domains[0] == "two.test" {
				<-releaseSecond
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{
				"success": true,
				"errors": [],
				"messages": [],
				"result": {
					"domains": [
						{
							"name": "` + request.Domains[0] + `",
							"registrable": true,
							"tier": "standard"
						}
					]
				}
			}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	defer close(releaseSecond)

	settings := DefaultSettings()
	settings.CacheEnabled = false
	settings.CloudflareAccountID = "account"
	settings.CloudflareAPIToken = "token"
	settings.CloudflareAPIBaseURL = server.URL
	settings.CloudflareBatchSize = 1
	settings.RateLimit = 0
	settings.BootstrapURL = server.URL + "/dns.json"
	settings.BootstrapCachePath = t.TempDir() + "/rdap.json"

	results, errs := RunChecks(t.Context(), settings, []Target{
		{Domain: "one.test"},
		{Domain: "two.test"},
	})

	select {
	case result := <-results:
		if result.Domain != "one.test" {
			t.Fatalf("got first domain %q", result.Domain)
		}
	case <-time.After(time.Second):
		t.Fatal("first batch did not stream before second batch finished")
	}

	select {
	case result := <-results:
		t.Fatalf("second result arrived before release: %#v", result)
	case <-time.After(100 * time.Millisecond):
	}

	releaseSecond <- struct{}{}
	select {
	case result := <-results:
		if result.Domain != "two.test" {
			t.Fatalf("got second domain %q", result.Domain)
		}
	case <-time.After(time.Second):
		t.Fatal("second batch did not finish after release")
	}

	if _, ok := <-results; ok {
		t.Fatal("expected results channel to be closed")
	}
	if err := <-errs; err != nil {
		t.Fatal(err)
	}
}

func TestRunChecksDoesNotCallCloudflareForRDAPRegistered(t *testing.T) {
	cloudflareCalls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/dns.json":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"services":[[["test"],["` + "http://" + r.Host + `/rdap/"]]]}`))
		case "/rdap/domain/taken.test":
			w.Header().Set("Content-Type", "application/rdap+json")
			_, _ = w.Write([]byte(`{
				"objectClassName": "domain",
				"ldhName": "taken.test",
				"status": ["active"],
				"events": [
					{"eventAction": "registration", "eventDate": "2020-01-01T00:00:00Z"},
					{"eventAction": "expiration", "eventDate": "2035-01-01T00:00:00Z"}
				]
			}`))
		case "/accounts/account/registrar/domain-check":
			cloudflareCalls++
			http.Error(w, "unexpected cloudflare call", http.StatusInternalServerError)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	settings := DefaultSettings()
	settings.CacheEnabled = false
	settings.CloudflareAccountID = "account"
	settings.CloudflareAPIToken = "token"
	settings.CloudflareAPIBaseURL = server.URL
	settings.CloudflareBatchSize = 20
	settings.RateLimit = 0
	settings.BootstrapURL = server.URL + "/dns.json"
	settings.BootstrapCachePath = t.TempDir() + "/rdap.json"

	results, errs := RunChecks(t.Context(), settings, []Target{{Domain: "taken.test"}})

	result := <-results
	if result.Domain != "taken.test" || result.Availability != AvailabilityRegistered {
		t.Fatalf("unexpected result: %#v", result)
	}
	if result.Confidence != "rdap_precheck" {
		t.Fatalf("got confidence %q", result.Confidence)
	}
	if _, ok := <-results; ok {
		t.Fatal("expected results channel to close")
	}
	if err := <-errs; err != nil {
		t.Fatal(err)
	}
	if cloudflareCalls != 0 {
		t.Fatalf("cloudflare calls: %d", cloudflareCalls)
	}
}

func TestRunChecksUsesHostingerForCloudflareUnknown(t *testing.T) {
	hostingerCalls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/dns.json":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"services":[[["test"],["` + "http://" + r.Host + `/rdap/"]]]}`))
		case "/rdap/domain/free.test":
			http.NotFound(w, r)
		case "/accounts/account/registrar/domain-check":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{
				"success": true,
				"errors": [],
				"messages": [],
				"result": { "domains": [] }
			}`))
		case "/api/domains/v1/availability":
			hostingerCalls++
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`[
				{
					"domain": "free.test",
					"is_available": true,
					"is_alternative": false,
					"restriction": null
				}
			]`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	settings := DefaultSettings()
	settings.CacheEnabled = false
	settings.CloudflareAccountID = "account"
	settings.CloudflareAPIToken = "token"
	settings.CloudflareAPIBaseURL = server.URL
	settings.CloudflareBatchSize = 20
	settings.HostingerAPIToken = "hostinger-token"
	settings.HostingerAPIBaseURL = server.URL
	settings.HostingerRateLimit = 0
	settings.RateLimit = 0
	settings.BootstrapURL = server.URL + "/dns.json"
	settings.BootstrapCachePath = t.TempDir() + "/rdap.json"

	results, errs := RunChecks(t.Context(), settings, []Target{{Domain: "free.test"}})

	result := <-results
	if result.Domain != "free.test" || result.Availability != AvailabilityAvailable {
		t.Fatalf("unexpected result: %#v", result)
	}
	if result.Confidence != "hostinger_authoritative" {
		t.Fatalf("got confidence %q", result.Confidence)
	}
	if result.HostingerAvailable == nil || !*result.HostingerAvailable {
		t.Fatalf("missing Hostinger availability: %#v", result)
	}
	if _, ok := <-results; ok {
		t.Fatal("expected results channel to close")
	}
	if err := <-errs; err != nil {
		t.Fatal(err)
	}
	if hostingerCalls != 1 {
		t.Fatalf("hostinger calls: %d", hostingerCalls)
	}
}

func TestRunChecksUsesVercelBeforeHostingerForCloudflareUnknown(t *testing.T) {
	vercelCalls := 0
	hostingerCalls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/dns.json":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"services":[[["test"],["` + "http://" + r.Host + `/rdap/"]]]}`))
		case "/rdap/domain/free.test":
			http.NotFound(w, r)
		case "/accounts/account/registrar/domain-check":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{
				"success": true,
				"errors": [],
				"messages": [],
				"result": { "domains": [] }
			}`))
		case "/v1/registrar/domains/availability":
			vercelCalls++
			if r.Header.Get("Authorization") != "Bearer vercel-token" {
				t.Fatalf("unexpected Vercel auth: %q", r.Header.Get("Authorization"))
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{
				"results": [
					{"domain": "free.test", "available": true}
				]
			}`))
		case "/api/domains/v1/availability":
			hostingerCalls++
			http.Error(w, "hostinger should not be called", http.StatusInternalServerError)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	settings := DefaultSettings()
	settings.CacheEnabled = false
	settings.CloudflareAccountID = "account"
	settings.CloudflareAPIToken = "token"
	settings.CloudflareAPIBaseURL = server.URL
	settings.CloudflareBatchSize = 20
	settings.VercelAPIToken = "vercel-token"
	settings.VercelAPIBaseURL = server.URL
	settings.VercelRateLimit = 0
	settings.HostingerAPIToken = "hostinger-token"
	settings.HostingerAPIBaseURL = server.URL
	settings.HostingerRateLimit = 0
	settings.RateLimit = 0
	settings.BootstrapURL = server.URL + "/dns.json"
	settings.BootstrapCachePath = t.TempDir() + "/rdap.json"

	results, errs := RunChecks(t.Context(), settings, []Target{{Domain: "free.test"}})

	result := <-results
	if result.Domain != "free.test" || result.Availability != AvailabilityAvailable {
		t.Fatalf("unexpected result: %#v", result)
	}
	if result.Confidence != "vercel_authoritative" {
		t.Fatalf("got confidence %q", result.Confidence)
	}
	if result.VercelAvailable == nil || !*result.VercelAvailable {
		t.Fatalf("missing Vercel availability: %#v", result)
	}
	if _, ok := <-results; ok {
		t.Fatal("expected results channel to close")
	}
	if err := <-errs; err != nil {
		t.Fatal(err)
	}
	if vercelCalls != 1 {
		t.Fatalf("vercel calls: %d", vercelCalls)
	}
	if hostingerCalls != 0 {
		t.Fatalf("hostinger calls: %d", hostingerCalls)
	}
}

func TestRunChecksFallsThroughToHostingerWhenVercelIsRateLimited(t *testing.T) {
	vercelCalls := 0
	hostingerCalls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/dns.json":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"services":[[["test"],["` + "http://" + r.Host + `/rdap/"]]]}`))
		case "/rdap/domain/free.test":
			http.NotFound(w, r)
		case "/accounts/account/registrar/domain-check":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{
				"success": true,
				"errors": [],
				"messages": [],
				"result": { "domains": [] }
			}`))
		case "/v1/registrar/domains/availability":
			vercelCalls++
			w.Header().Set("Retry-After", "60")
			http.Error(w, "slow down", http.StatusTooManyRequests)
		case "/api/domains/v1/availability":
			hostingerCalls++
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`[
				{
					"domain": "free.test",
					"is_available": true,
					"is_alternative": false,
					"restriction": null
				}
			]`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	settings := DefaultSettings()
	settings.CacheEnabled = false
	settings.CloudflareAccountID = "account"
	settings.CloudflareAPIToken = "token"
	settings.CloudflareAPIBaseURL = server.URL
	settings.CloudflareBatchSize = 20
	settings.VercelAPIToken = "vercel-token"
	settings.VercelAPIBaseURL = server.URL
	settings.VercelRateLimit = 0
	settings.HostingerAPIToken = "hostinger-token"
	settings.HostingerAPIBaseURL = server.URL
	settings.HostingerRateLimit = 0
	settings.SourceRateLimitMaxDelay = 5 * time.Second
	settings.RateLimit = 0
	settings.BootstrapURL = server.URL + "/dns.json"
	settings.BootstrapCachePath = t.TempDir() + "/rdap.json"

	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	results, errs := RunChecks(ctx, settings, []Target{{Domain: "free.test"}})

	var result Result
	select {
	case result = <-results:
	case <-ctx.Done():
		t.Fatal("runner waited on Vercel rate limit instead of falling through to Hostinger")
	}
	if result.Domain != "free.test" || result.Availability != AvailabilityAvailable {
		t.Fatalf("unexpected result: %#v", result)
	}
	if result.Confidence != "hostinger_authoritative" {
		t.Fatalf("got confidence %q", result.Confidence)
	}
	if result.HostingerAvailable == nil || !*result.HostingerAvailable {
		t.Fatalf("missing Hostinger availability: %#v", result)
	}
	if _, ok := <-results; ok {
		t.Fatal("expected results channel to close")
	}
	if err := <-errs; err != nil {
		t.Fatal(err)
	}
	if vercelCalls != 1 {
		t.Fatalf("vercel calls: %d", vercelCalls)
	}
	if hostingerCalls != 1 {
		t.Fatalf("hostinger calls: %d", hostingerCalls)
	}
}

func TestRunChecksRetriesCloudflareAfterRateLimit(t *testing.T) {
	cloudflareCalls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/dns.json":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"services":[[["test"],["` + "http://" + r.Host + `/rdap/"]]]}`))
		case "/rdap/domain/free.test":
			http.NotFound(w, r)
		case "/accounts/account/registrar/domain-check":
			cloudflareCalls++
			if cloudflareCalls == 1 {
				w.Header().Set("Retry-After", "0")
				http.Error(w, "rate limited", http.StatusTooManyRequests)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{
				"success": true,
				"errors": [],
				"messages": [],
				"result": {
					"domains": [
						{"name": "free.test", "registrable": true}
					]
				}
			}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	settings := DefaultSettings()
	settings.CacheEnabled = false
	settings.CloudflareAccountID = "account"
	settings.CloudflareAPIToken = "token"
	settings.CloudflareAPIBaseURL = server.URL
	settings.CloudflareBatchSize = 20
	settings.SourceRateLimitRetries = 1
	settings.RateLimit = 0
	settings.BootstrapURL = server.URL + "/dns.json"
	settings.BootstrapCachePath = t.TempDir() + "/rdap.json"

	results, errs := RunChecks(t.Context(), settings, []Target{{Domain: "free.test"}})

	result := <-results
	if result.Domain != "free.test" || result.Availability != AvailabilityAvailable {
		t.Fatalf("unexpected result: %#v", result)
	}
	if _, ok := <-results; ok {
		t.Fatal("expected results channel to close")
	}
	if err := <-errs; err != nil {
		t.Fatal(err)
	}
	if cloudflareCalls != 2 {
		t.Fatalf("cloudflare calls: %d", cloudflareCalls)
	}
}

func TestRunChecksUsesReservedCacheWithoutNetwork(t *testing.T) {
	path := t.TempDir() + "/results.json"
	data, err := json.Marshal(cacheFile{
		Version: 1,
		Entries: map[string]Result{
			"taken.test": {
				Domain:       "taken.test",
				Availability: AvailabilityReserved,
				Lifecycle:    "not_registrable",
				Confidence:   "cloudflare_authoritative",
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}

	settings := DefaultSettings()
	settings.CachePath = path
	settings.CloudflareAccountID = "account"
	settings.CloudflareAPIToken = "token"

	results, errs := RunChecks(t.Context(), settings, []Target{{Domain: "taken.test", Input: "taken", LineNumber: 4}})

	result := <-results
	if result.Domain != "taken.test" || result.Availability != AvailabilityReserved || !result.CacheHit {
		t.Fatalf("unexpected result: %#v", result)
	}
	if result.LineNumber != 4 {
		t.Fatalf("line number not refreshed from input: %#v", result)
	}
	if _, ok := <-results; ok {
		t.Fatal("expected results channel to close")
	}
	if err := <-errs; err != nil {
		t.Fatal(err)
	}
}

func TestRunChecksDoesNotRetryHostingerRateLimitPerDomain(t *testing.T) {
	hostingerCalls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/dns.json":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"services":[[["test"],["` + "http://" + r.Host + `/rdap/"]]]}`))
		case "/rdap/domain/one.test", "/rdap/domain/two.test", "/rdap/domain/three.test":
			http.NotFound(w, r)
		case "/accounts/account/registrar/domain-check":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{
				"success": true,
				"errors": [],
				"messages": [],
				"result": { "domains": [] }
			}`))
		case "/api/domains/v1/availability":
			hostingerCalls++
			w.Header().Set("Retry-After", "0")
			http.Error(w, "rate limited", http.StatusTooManyRequests)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	settings := DefaultSettings()
	settings.CacheEnabled = false
	settings.CloudflareAccountID = "account"
	settings.CloudflareAPIToken = "token"
	settings.CloudflareAPIBaseURL = server.URL
	settings.CloudflareBatchSize = 20
	settings.HostingerAPIToken = "hostinger-token"
	settings.HostingerAPIBaseURL = server.URL
	settings.HostingerRateLimit = 0
	settings.SourceRateLimitRetries = 1
	settings.RateLimit = 0
	settings.BootstrapURL = server.URL + "/dns.json"
	settings.BootstrapCachePath = t.TempDir() + "/rdap.json"

	results, errs := RunChecks(t.Context(), settings, []Target{
		{Domain: "one.test"},
		{Domain: "two.test"},
		{Domain: "three.test"},
	})

	var got []Result
	for result := range results {
		got = append(got, result)
	}
	if err := <-errs; err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 {
		t.Fatalf("got %d results: %#v", len(got), got)
	}
	for _, result := range got {
		if result.Availability != AvailabilityRateLimited {
			t.Fatalf("expected rate limited result, got %#v", result)
		}
	}
	if hostingerCalls != 2 {
		t.Fatalf("hostinger calls should be source-level retries, got %d", hostingerCalls)
	}
}

func TestTargetBatchesCapsAtCloudflareLimit(t *testing.T) {
	targets := make([]Target, 21)
	batches := targetBatches(targets, 99)
	if len(batches) != 2 {
		t.Fatalf("got %d batches", len(batches))
	}
	if len(batches[0]) != 20 || len(batches[1]) != 1 {
		t.Fatalf("unexpected batch sizes: %d %d", len(batches[0]), len(batches[1]))
	}
}
