package api

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"qda/internal/config"
	"qda/internal/output"
	"qda/internal/runner"
	"qda/internal/sources"
	"qda/internal/store"
)

func testServer(t *testing.T, token string) (*httptest.Server, *store.Store) {
	t.Helper()

	mux := http.NewServeMux()
	mux.HandleFunc("/domain/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/domain/taken.net" {
			w.Header().Set("Content-Type", "application/rdap+json")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"objectClassName": "domain",
				"ldhName":         "taken.net",
				"status":          []string{"active"},
			})
			return
		}
		w.WriteHeader(http.StatusNotFound)
	})
	rdapServer := httptest.NewServer(mux)
	t.Cleanup(rdapServer.Close)

	bootstrapServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(sources.RDAPBootstrap{
			Services: [][][]string{{{"net"}, {rdapServer.URL + "/"}}},
		})
	}))
	t.Cleanup(bootstrapServer.Close)

	settings := config.Default()
	settings.RDAP.BootstrapURL = bootstrapServer.URL
	settings.RDAP.BootstrapCachePath = ""
	settings.NetworkRetries = 0
	settings.RateLimit = 0

	printer := output.New(io.Discard, io.Discard, output.LevelSilent, false)
	db, err := store.Open(filepath.Join(t.TempDir(), "db.json"), true, 168*time.Hour, 720*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	engine, err := runner.NewEngine(settings, db, printer)
	if err != nil {
		t.Fatal(err)
	}
	if err := engine.Prepare(context.Background()); err != nil {
		t.Fatal(err)
	}

	server := NewServer(engine, db, printer, token, 30)
	return httptest.NewServer(server.Handler()), db
}

func TestHealth(t *testing.T) {
	server, _ := testServer(t, "")
	defer server.Close()

	resp, err := http.Get(server.URL + "/health")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
}

func TestAuthRequired(t *testing.T) {
	server, _ := testServer(t, "secret")
	defer server.Close()

	resp, err := http.Get(server.URL + "/v1/stats")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status without token = %d, want 401", resp.StatusCode)
	}

	req, _ := http.NewRequest(http.MethodGet, server.URL+"/v1/stats", nil)
	req.Header.Set("X-API-Key", "secret")
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status with token = %d", resp.StatusCode)
	}
}

func TestCheckLiveAndStore(t *testing.T) {
	server, db := testServer(t, "")
	defer server.Close()

	resp, err := http.Get(server.URL + "/v1/check?domain=free.net")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}

	var result map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		t.Fatal(err)
	}
	if result["availability"] != "available" {
		t.Fatalf("availability = %v", result["availability"])
	}

	if _, ok := db.Get("free.net"); !ok {
		t.Fatal("result was not persisted to the store")
	}
}

func TestCheckRegisteredAndDomainsEndpoint(t *testing.T) {
	server, _ := testServer(t, "")
	defer server.Close()

	resp, err := http.Get(server.URL + "/v1/check?domain=taken.net")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()

	resp, err = http.Get(server.URL + "/v1/domains?status=registered")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	var payload struct {
		Count   int `json:"count"`
		Records []struct {
			Domain string `json:"domain"`
		} `json:"records"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		t.Fatal(err)
	}
	if payload.Count != 1 || payload.Records[0].Domain != "taken.net" {
		t.Fatalf("payload = %+v", payload)
	}
}

func TestCheckRejectsInvalidDomain(t *testing.T) {
	server, _ := testServer(t, "")
	defer server.Close()

	resp, err := http.Get(server.URL + "/v1/check?domain=not_a_domain")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", resp.StatusCode)
	}
}
