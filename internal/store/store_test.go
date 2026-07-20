package store

import (
	"path/filepath"
	"testing"
	"time"

	"qda/internal/types"
)

func testStore(t *testing.T) *Store {
	t.Helper()
	db, err := Open(filepath.Join(t.TempDir(), "db.json"), true, 168*time.Hour, 720*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	return db
}

func registeredResult(domain string, expiresAt string) types.Result {
	return types.Result{
		Domain:       domain,
		Availability: types.AvailabilityRegistered,
		Lifecycle:    "active",
		ExpiresAt:    expiresAt,
		CheckedAt:    time.Now().UTC(),
	}
}

func TestPutAndGet(t *testing.T) {
	db := testStore(t)
	db.Put(registeredResult("example.com", ""))
	record, ok := db.Get("example.com")
	if !ok {
		t.Fatal("record not found")
	}
	if record.CheckCount != 1 {
		t.Fatalf("check count = %d, want 1", record.CheckCount)
	}

	first := record.FirstSeenAt
	db.Put(registeredResult("example.com", ""))
	record, _ = db.Get("example.com")
	if record.CheckCount != 2 {
		t.Fatalf("check count = %d, want 2", record.CheckCount)
	}
	if !record.FirstSeenAt.Equal(first) {
		t.Fatal("first_seen_at changed on update")
	}
}

func TestReusableRegistered(t *testing.T) {
	db := testStore(t)
	future := time.Now().Add(90 * 24 * time.Hour).Format(time.RFC3339)
	db.Put(registeredResult("taken.com", future))

	if _, ok := db.Reusable("taken.com", time.Now().UTC()); !ok {
		t.Fatal("registered domain should be reusable")
	}

	past := time.Now().Add(-24 * time.Hour).Format(time.RFC3339)
	db.Put(types.Result{
		Domain:       "expired.com",
		Availability: types.AvailabilityRegistered,
		Lifecycle:    "expired_or_grace",
		ExpiresAt:    past,
		CheckedAt:    time.Now().UTC().Add(-48 * time.Hour),
	})
	if _, ok := db.Reusable("expired.com", time.Now().UTC()); ok {
		t.Fatal("expired domain should not be reusable")
	}
}

func TestRegisteredTTLBoundsReuse(t *testing.T) {
	db := testStore(t)
	db.Put(types.Result{
		Domain:       "old.com",
		Availability: types.AvailabilityRegistered,
		Lifecycle:    "active",
		CheckedAt:    time.Now().UTC().Add(-200 * time.Hour), // older than 168h TTL
	})
	if _, ok := db.Reusable("old.com", time.Now().UTC()); ok {
		t.Fatal("record older than registered_ttl should not be reusable")
	}
}

func TestAvailableIsNeverReused(t *testing.T) {
	db := testStore(t)
	db.Put(types.Result{
		Domain:       "maybe.com",
		Availability: types.AvailabilityAvailable,
		Lifecycle:    "available",
		CheckedAt:    time.Now().UTC(),
	})
	if _, ok := db.Reusable("maybe.com", time.Now().UTC()); ok {
		t.Fatal("available domains must always be rechecked")
	}
}

func TestSaveAndReload(t *testing.T) {
	path := filepath.Join(t.TempDir(), "db.json")
	db, err := Open(path, true, 168*time.Hour, 720*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	db.Put(registeredResult("persist.com", ""))
	if err := db.Save(); err != nil {
		t.Fatal(err)
	}
	if db.Dirty() {
		t.Fatal("store still dirty after save")
	}

	reloaded, err := Open(path, true, 168*time.Hour, 720*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := reloaded.Get("persist.com"); !ok {
		t.Fatal("record missing after reload")
	}
}

func TestQueryFilters(t *testing.T) {
	db := testStore(t)
	now := time.Now().UTC()
	db.Put(types.Result{Domain: "alpha.net", Availability: types.AvailabilityAvailable, Lifecycle: "available", CheckedAt: now})
	db.Put(types.Result{Domain: "beta.net", Availability: types.AvailabilityRegistered, Lifecycle: "active", CheckedAt: now})
	db.Put(types.Result{Domain: "gamma.org", Availability: types.AvailabilityRegistered, Lifecycle: "expiring_soon", ExpiringSoon: true, ExpiresAt: now.Add(72 * time.Hour).Format(time.RFC3339), CheckedAt: now})

	if got := db.Query(Query{Status: "available"}); len(got) != 1 || got[0].Domain != "alpha.net" {
		t.Fatalf("status filter: %+v", got)
	}
	if got := db.Query(Query{TLD: "net"}); len(got) != 2 {
		t.Fatalf("tld filter: %+v", got)
	}
	if got := db.Query(Query{Contains: "gamma"}); len(got) != 1 {
		t.Fatalf("contains filter: %+v", got)
	}
	days := 30
	if got := db.Query(Query{ExpiringIn: &days}); len(got) != 1 || got[0].Domain != "gamma.org" {
		t.Fatalf("expiring filter: %+v", got)
	}
	if got := db.Query(Query{AvailableSoon: true}); len(got) != 2 {
		t.Fatalf("available-soon filter: %+v", got)
	}
}

func TestStats(t *testing.T) {
	db := testStore(t)
	now := time.Now().UTC()
	db.Put(types.Result{Domain: "a.net", Availability: types.AvailabilityAvailable, Lifecycle: "available", CheckedAt: now})
	db.Put(types.Result{Domain: "b.net", Availability: types.AvailabilityRegistered, Lifecycle: "active", CheckedAt: now})

	stats := db.Stats()
	if stats.Total != 2 || stats.Available != 1 {
		t.Fatalf("stats: %+v", stats)
	}
	if stats.ByAvailability["registered"] != 1 {
		t.Fatalf("stats by availability: %+v", stats.ByAvailability)
	}
}
