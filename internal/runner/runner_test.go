package runner

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"qda/internal/config"
	"qda/internal/output"
	"qda/internal/resume"
	"qda/internal/sources"
	"qda/internal/store"
	"qda/internal/types"
)

type rdapFixture struct {
	settings config.Settings
	cleanup  func()
	slowHits *atomic.Int64
}

func newRDAPFixture(t *testing.T) *rdapFixture {
	t.Helper()
	fixture := &rdapFixture{slowHits: &atomic.Int64{}}

	mux := http.NewServeMux()
	mux.HandleFunc("/domain/", func(w http.ResponseWriter, r *http.Request) {
		domain := r.URL.Path[len("/domain/"):]
		switch domain {
		case "taken.net":
			w.Header().Set("Content-Type", "application/rdap+json")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"objectClassName": "domain",
				"ldhName":         "taken.net",
				"status":          []string{"active"},
			})
		case "slow.net":
			// Rate-limit the first two hits, then answer available.
			if fixture.slowHits.Add(1) <= 2 {
				w.Header().Set("Retry-After", "1")
				w.WriteHeader(http.StatusTooManyRequests)
				return
			}
			w.WriteHeader(http.StatusNotFound)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	})
	server := httptest.NewServer(mux)

	bootstrapServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(sources.RDAPBootstrap{
			Services: [][][]string{{{"net"}, {server.URL + "/"}}},
		})
	}))

	settings := config.Default()
	settings.RDAP.BootstrapURL = bootstrapServer.URL
	settings.RDAP.BootstrapCachePath = ""
	settings.NetworkRetries = 0
	settings.RateLimit = 0
	settings.SourceFreeze = time.Second
	settings.MaxAttempts = 4
	settings.ProgressInterval = time.Hour // no progress noise in tests

	fixture.settings = settings
	fixture.cleanup = func() {
		server.Close()
		bootstrapServer.Close()
	}
	return fixture
}

func newTestRunner(t *testing.T, settings config.Settings) (*Runner, *store.Store) {
	t.Helper()
	printer := output.New(io.Discard, io.Discard, output.LevelSilent, false)
	db, err := store.Open(filepath.Join(t.TempDir(), "db.json"), true, 168*time.Hour, 720*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	engine, err := NewEngine(settings, db, printer)
	if err != nil {
		t.Fatal(err)
	}
	if err := engine.Prepare(context.Background()); err != nil {
		t.Fatal(err)
	}
	resumeManager := resume.New(filepath.Join(t.TempDir(), "state.json"), "hash", settings.TLDs, nil)
	return NewRunner(engine, db, printer, resumeManager), db
}

func resultsByDomain(results []types.Result) map[string]types.Result {
	out := map[string]types.Result{}
	for _, result := range results {
		out[result.Domain] = result
	}
	return out
}

func TestRunStreamsAllResults(t *testing.T) {
	fixture := newRDAPFixture(t)
	defer fixture.cleanup()

	runner, _ := newTestRunner(t, fixture.settings)
	targets := []types.Target{
		{Domain: "free.net", Input: "free"},
		{Domain: "taken.net", Input: "taken"},
	}

	results, err := runner.Run(context.Background(), targets)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 2 {
		t.Fatalf("got %d results, want 2", len(results))
	}

	byDomain := resultsByDomain(results)
	if byDomain["free.net"].Availability != types.AvailabilityAvailable {
		t.Fatalf("free.net = %s", byDomain["free.net"].Availability)
	}
	if byDomain["taken.net"].Availability != types.AvailabilityRegistered {
		t.Fatalf("taken.net = %s", byDomain["taken.net"].Availability)
	}
}

func TestRateLimitedItemIsRequeuedAndEventuallyCompletes(t *testing.T) {
	fixture := newRDAPFixture(t)
	defer fixture.cleanup()

	runner, _ := newTestRunner(t, fixture.settings)
	targets := []types.Target{{Domain: "slow.net", Input: "slow"}}

	results, err := runner.Run(context.Background(), targets)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 {
		t.Fatalf("got %d results, want 1", len(results))
	}
	if results[0].Availability != types.AvailabilityAvailable {
		t.Fatalf("slow.net = %s, want available after requeue", results[0].Availability)
	}
	if runner.Stats().Requeued.Load() < 1 {
		t.Fatal("expected at least one requeue")
	}
	if got := fixture.slowHits.Load(); got != 3 {
		t.Fatalf("server hits = %d, want 3 (2 rate-limited + 1 success)", got)
	}
}

func TestStoreReuseSkipsLiveCheck(t *testing.T) {
	fixture := newRDAPFixture(t)
	defer fixture.cleanup()

	runner, db := newTestRunner(t, fixture.settings)
	db.Put(types.Result{
		Domain:       "taken.net",
		Availability: types.AvailabilityRegistered,
		Lifecycle:    "active",
		CheckedAt:    time.Now().UTC(),
	})

	results, err := runner.Run(context.Background(), []types.Target{{Domain: "taken.net", Input: "taken"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 || !results[0].CacheHit {
		t.Fatalf("expected cache hit, got %+v", results)
	}
}

func TestResumeStateTracksPending(t *testing.T) {
	fixture := newRDAPFixture(t)
	defer fixture.cleanup()

	printer := output.New(io.Discard, io.Discard, output.LevelSilent, false)
	db, err := store.Open(filepath.Join(t.TempDir(), "db.json"), true, 168*time.Hour, 720*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	engine, err := NewEngine(fixture.settings, db, printer)
	if err != nil {
		t.Fatal(err)
	}
	if err := engine.Prepare(context.Background()); err != nil {
		t.Fatal(err)
	}

	statePath := filepath.Join(t.TempDir(), "state.json")
	resumeManager := resume.New(statePath, "hash", fixture.settings.TLDs, []string{"free.net", "taken.net"})
	runner := NewRunner(engine, db, printer, resumeManager)

	targets := []types.Target{{Domain: "free.net"}, {Domain: "taken.net"}}
	if _, err := runner.Run(context.Background(), targets); err != nil {
		t.Fatal(err)
	}

	state := resumeManager.State()
	if len(state.Pending) != 0 {
		t.Fatalf("pending after run = %v", state.Pending)
	}
	if state.Completed != 2 {
		t.Fatalf("completed = %d, want 2", state.Completed)
	}
}

// TestChainCascadesPastUnsupportedSource covers the .lat-style path:
// RDAP unknown/available → Cloudflare extension_not_supported → Vercel definitive.
func TestChainCascadesPastUnsupportedSource(t *testing.T) {
	var cfHits, vercelHits atomic.Int64

	rdapMux := http.NewServeMux()
	rdapMux.HandleFunc("/domain/", func(w http.ResponseWriter, r *http.Request) {
		// Simulate sparse/unknown-friendly TLD behaviour: RDAP 404 available.
		w.WriteHeader(http.StatusNotFound)
	})
	rdapServer := httptest.NewServer(rdapMux)
	defer rdapServer.Close()

	bootstrapServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(sources.RDAPBootstrap{
			Services: [][][]string{{{"lat"}, {rdapServer.URL + "/"}}},
		})
	}))
	defer bootstrapServer.Close()

	registrarMux := http.NewServeMux()
	// Cloudflare Registrar domain-check
	registrarMux.HandleFunc("/accounts/", func(w http.ResponseWriter, r *http.Request) {
		cfHits.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"success": true,
			"result": map[string]any{
				"domains": []map[string]any{{
					"name":        "free.lat",
					"registrable": false,
					"reason":      "extension_not_supported",
				}},
			},
		})
	})
	// Vercel Registrar availability
	registrarMux.HandleFunc("/v1/registrar/domains/availability", func(w http.ResponseWriter, r *http.Request) {
		vercelHits.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"results": []map[string]any{{
				"domain":    "free.lat",
				"available": true,
			}},
		})
	})
	registrarServer := httptest.NewServer(registrarMux)
	defer registrarServer.Close()

	settings := config.Default()
	settings.RDAPOnly = false
	settings.BRRDAPOnly = false
	settings.NetworkRetries = 0
	settings.RateLimit = 0
	settings.MaxAttempts = 2
	settings.ProgressInterval = time.Hour
	settings.RDAP.BootstrapURL = bootstrapServer.URL
	settings.RDAP.BootstrapCachePath = ""
	settings.Cloudflare.AccountID = "test-account"
	settings.Cloudflare.APIToken = "cf-token"
	settings.Cloudflare.APIBaseURL = registrarServer.URL
	settings.Cloudflare.RateLimit = 0
	settings.Vercel.APIToken = "vercel-token"
	settings.Vercel.APIBaseURL = registrarServer.URL
	settings.Vercel.RateLimit = 0

	runner, _ := newTestRunner(t, settings)
	results, err := runner.Run(context.Background(), []types.Target{{Domain: "free.lat", Input: "free"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 {
		t.Fatalf("got %d results, want 1", len(results))
	}
	got := results[0]
	if got.Availability != types.AvailabilityAvailable {
		t.Fatalf("availability = %s, want available (vercel should win)", got.Availability)
	}
	if got.Source == "" || !containsSourceName(got, "vercel") {
		t.Fatalf("expected vercel in sources, got source=%q sources=%+v", got.Source, got.Sources)
	}
	if cfHits.Load() < 1 {
		t.Fatal("expected cloudflare to be queried")
	}
	if vercelHits.Load() < 1 {
		t.Fatal("expected vercel to be queried after cloudflare unknown")
	}
}

func containsSourceName(result types.Result, name string) bool {
	for _, src := range result.Sources {
		if src.Name == name {
			return true
		}
	}
	return result.Source == name
}
