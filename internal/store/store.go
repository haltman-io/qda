// Package store implements the local results database: one JSON document
// with one record per domain, atomically persisted, plus query helpers used
// by the scan runner, the `db` subcommand and the API server.
package store

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"qda/internal/domainx"
	"qda/internal/types"
)

// Record is the persisted state of one domain.
type Record struct {
	Domain        string             `json:"domain"`
	Availability  types.Availability `json:"availability"`
	Lifecycle     string             `json:"lifecycle,omitempty"`
	Confidence    string             `json:"confidence,omitempty"`
	Registrar     string             `json:"registrar,omitempty"`
	CreatedAt     string             `json:"created_at,omitempty"`
	ExpiresAt     string             `json:"expires_at,omitempty"`
	UpdatedAt     string             `json:"updated_at,omitempty"`
	Statuses      []string           `json:"statuses,omitempty"`
	Source        string             `json:"source,omitempty"`
	Error         string             `json:"error,omitempty"`
	Price         *types.Pricing     `json:"price,omitempty"`
	ExpiringSoon  bool               `json:"expiring_soon,omitempty"`
	ExpiresInDays *int               `json:"expires_in_days,omitempty"`
	FirstSeenAt   time.Time          `json:"first_seen_at"`
	LastCheckedAt time.Time          `json:"last_checked_at"`
	CheckCount    int                `json:"check_count"`
}

// Query filters stored records.
type Query struct {
	// Status filters by availability (available, registered, ...).
	Status string
	// TLD filters by public suffix ("net", "com.br", ...).
	TLD string
	// Contains matches a substring of the domain.
	Contains string
	// ExpiringIn keeps only records expiring within N days.
	ExpiringIn *int
	// AvailableSoon keeps available, premium and soon-to-drop domains.
	AvailableSoon bool
}

// Stats aggregates database counters.
type Stats struct {
	Total           int            `json:"total"`
	ByAvailability  map[string]int `json:"by_availability"`
	ExpiringSoon    int            `json:"expiring_soon"`
	Available       int            `json:"available"`
	AvailableSoon   int            `json:"available_soon"`
	LastCheckedAt   time.Time      `json:"last_checked_at,omitempty"`
}

// Store is a concurrency-safe JSON document database.
type Store struct {
	mu            sync.Mutex
	path          string
	enabled       bool
	registeredTTL time.Duration
	reservedTTL   time.Duration
	records       map[string]*Record
	dirty         bool
	warning       string
}

type diskFormat struct {
	Version int                `json:"version"`
	Records map[string]*Record `json:"records"`
}

// legacyDiskFormat is the pre-0.2 cache layout ("entries" of full results).
type legacyDiskFormat struct {
	Version int                       `json:"version"`
	Entries map[string]legacyEntry    `json:"entries"`
}

type legacyEntry struct {
	Domain        string             `json:"domain"`
	Availability  types.Availability `json:"availability"`
	Lifecycle     string             `json:"lifecycle"`
	Confidence    string             `json:"confidence"`
	CreatedAt     string             `json:"created_at"`
	ExpiresAt     string             `json:"expires_at"`
	UpdatedAt     string             `json:"updated_at"`
	Statuses      []string           `json:"statuses"`
	Registrar     string             `json:"registrar"`
	Source        string             `json:"source"`
	Error         string             `json:"error"`
	CheckedAt     time.Time          `json:"checked_at"`
	ExpiringSoon  bool               `json:"expiring_soon"`
	ExpiresInDays *int               `json:"expires_in_days"`
}

// Open loads the database (an absent file is not an error).
func Open(path string, enabled bool, registeredTTL time.Duration, reservedTTL time.Duration) (*Store, error) {
	s := &Store{
		path:          path,
		enabled:       enabled,
		registeredTTL: registeredTTL,
		reservedTTL:   reservedTTL,
		records:       map[string]*Record{},
	}
	if !enabled {
		return s, nil
	}
	if strings.TrimSpace(path) == "" {
		return nil, errors.New("store path is empty")
	}

	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return s, nil
		}
		return nil, fmt.Errorf("read store: %w", err)
	}

	var file diskFormat
	if err := json.Unmarshal(data, &file); err != nil {
		s.warning = "ignoring invalid store file: " + err.Error()
		return s, nil
	}
	for domain, record := range file.Records {
		if record == nil || record.Domain == "" {
			continue
		}
		s.records[domain] = record
	}
	if len(file.Records) == 0 {
		s.migrateLegacy(data)
	}
	return s, nil
}

// migrateLegacy imports records from the pre-0.2 cache format so existing
// users keep their history.
func (s *Store) migrateLegacy(data []byte) {
	var legacy legacyDiskFormat
	if err := json.Unmarshal(data, &legacy); err != nil || len(legacy.Entries) == 0 {
		return
	}
	for domain, entry := range legacy.Entries {
		if entry.Domain == "" || entry.CheckedAt.IsZero() {
			continue
		}
		s.records[domain] = &Record{
			Domain:        entry.Domain,
			Availability:  entry.Availability,
			Lifecycle:     entry.Lifecycle,
			Confidence:    entry.Confidence,
			CreatedAt:     entry.CreatedAt,
			ExpiresAt:     entry.ExpiresAt,
			UpdatedAt:     entry.UpdatedAt,
			Statuses:      entry.Statuses,
			Registrar:     entry.Registrar,
			Source:        entry.Source,
			Error:         entry.Error,
			ExpiringSoon:  entry.ExpiringSoon,
			ExpiresInDays: entry.ExpiresInDays,
			FirstSeenAt:   entry.CheckedAt,
			LastCheckedAt: entry.CheckedAt,
			CheckCount:    1,
		}
	}
	if len(s.records) > 0 {
		s.warning = fmt.Sprintf("migrated %d records from the legacy cache format", len(s.records))
		s.dirty = true
	}
}

// Warning reports a non-fatal load problem.
func (s *Store) Warning() string { return s.warning }

// Path returns the database file path.
func (s *Store) Path() string { return s.path }

// Len returns how many domains are stored.
func (s *Store) Len() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.records)
}

// Put upserts a check result.
func (s *Store) Put(result types.Result) {
	if s == nil || !s.enabled || result.Domain == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	record, ok := s.records[result.Domain]
	if !ok {
		record = &Record{Domain: result.Domain, FirstSeenAt: result.CheckedAt}
		s.records[result.Domain] = record
	}
	if record.FirstSeenAt.IsZero() || result.CheckedAt.Before(record.FirstSeenAt) {
		record.FirstSeenAt = result.CheckedAt
	}

	record.Availability = result.Availability
	record.Lifecycle = result.Lifecycle
	record.Confidence = result.Confidence
	record.Registrar = result.Registrar
	record.CreatedAt = result.CreatedAt
	record.ExpiresAt = result.ExpiresAt
	record.UpdatedAt = result.UpdatedAt
	record.Statuses = append([]string(nil), result.Statuses...)
	record.Source = result.Source
	record.Error = result.Error
	record.Price = result.Price
	record.ExpiringSoon = result.ExpiringSoon
	record.ExpiresInDays = result.ExpiresInDays
	record.LastCheckedAt = result.CheckedAt
	record.CheckCount++
	s.dirty = true
}

// Get returns one record by domain.
func (s *Store) Get(domain string) (*Record, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	record, ok := s.records[strings.ToLower(domain)]
	if !ok {
		return nil, false
	}
	copy := *record
	return &copy, true
}

// Reusable returns the stored record when it is fresh enough to skip a
// live check. Registered domains are reused until expiration (bounded by
// registeredTTL); reserved domains for reservedTTL. Everything else
// (available, premium, pending_delete, redemption, rate_limited, unknown)
// is always checked live because it can change at any moment.
func (s *Store) Reusable(domain string, now time.Time) (*Record, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	record, ok := s.records[strings.ToLower(domain)]
	if !ok {
		return nil, false
	}
	if now.Sub(record.LastCheckedAt) < 0 {
		return nil, false
	}
	switch record.Availability {
	case types.AvailabilityRegistered:
		if s.registeredTTL > 0 && now.Sub(record.LastCheckedAt) > s.registeredTTL {
			return nil, false
		}
		if record.ExpiresAt != "" {
			expiresAt, err := time.Parse(time.RFC3339, record.ExpiresAt)
			if err != nil || !expiresAt.After(now) {
				return nil, false
			}
		}
		copy := *record
		return &copy, true
	case types.AvailabilityReserved:
		if s.reservedTTL > 0 && now.Sub(record.LastCheckedAt) > s.reservedTTL {
			return nil, false
		}
		copy := *record
		return &copy, true
	default:
		return nil, false
	}
}

// ToResult converts a stored record back into a Result marked as cache hit,
// recomputing lifecycle relative to now (expiration drift is applied).
func ToResult(record *Record, expiringSoonDays int, now time.Time) types.Result {
	result := types.Result{
		Domain:       record.Domain,
		Availability: record.Availability,
		Confidence:   record.Confidence,
		CreatedAt:    record.CreatedAt,
		ExpiresAt:    record.ExpiresAt,
		UpdatedAt:    record.UpdatedAt,
		Statuses:     append([]string(nil), record.Statuses...),
		Registrar:    record.Registrar,
		Source:       record.Source,
		Error:        record.Error,
		Price:        record.Price,
		CheckedAt:    record.LastCheckedAt,
		CacheHit:     true,
	}
	result.Lifecycle, result.ExpiringSoon, result.ExpiresInDays = types.DetermineLifecycle(
		record.Availability, record.Statuses, record.ExpiresAt, expiringSoonDays, now)
	return result
}

// Query returns matching records sorted available-first, then by domain.
func (s *Store) Query(q Query) []*Record {
	s.mu.Lock()
	defer s.mu.Unlock()

	now := time.Now().UTC()
	var out []*Record
	for _, record := range s.records {
		if !matches(record, q, now) {
			continue
		}
		copy := *record
		out = append(out, &copy)
	}
	sort.Slice(out, func(i, j int) bool {
		left := types.SortRank(types.Result{Availability: out[i].Availability, Lifecycle: out[i].Lifecycle, ExpiringSoon: out[i].ExpiringSoon})
		right := types.SortRank(types.Result{Availability: out[j].Availability, Lifecycle: out[j].Lifecycle, ExpiringSoon: out[j].ExpiringSoon})
		if left != right {
			return left < right
		}
		return out[i].Domain < out[j].Domain
	})
	return out
}

func matches(record *Record, q Query, now time.Time) bool {
	if q.Status != "" && string(record.Availability) != strings.ToLower(q.Status) {
		return false
	}
	if q.TLD != "" {
		tld := domainx.TLDOf(record.Domain)
		if !strings.EqualFold(tld, strings.TrimPrefix(q.TLD, ".")) {
			return false
		}
	}
	if q.Contains != "" && !strings.Contains(record.Domain, strings.ToLower(q.Contains)) {
		return false
	}
	if q.ExpiringIn != nil {
		if record.ExpiresAt == "" {
			return false
		}
		expiresAt, err := time.Parse(time.RFC3339, record.ExpiresAt)
		if err != nil {
			return false
		}
		days := int(expiresAt.Sub(now).Hours() / 24)
		if days < 0 || days > *q.ExpiringIn {
			return false
		}
	}
	if q.AvailableSoon {
		result := types.Result{Availability: record.Availability, Lifecycle: record.Lifecycle, ExpiringSoon: record.ExpiringSoon}
		if !types.IsAvailableLike(result) && !types.IsAvailableSoon(result) {
			return false
		}
	}
	return true
}

// Stats computes aggregate counters.
func (s *Store) Stats() Stats {
	s.mu.Lock()
	defer s.mu.Unlock()

	stats := Stats{ByAvailability: map[string]int{}}
	for _, record := range s.records {
		stats.Total++
		stats.ByAvailability[string(record.Availability)]++
		result := types.Result{Availability: record.Availability, Lifecycle: record.Lifecycle, ExpiringSoon: record.ExpiringSoon}
		if record.ExpiringSoon {
			stats.ExpiringSoon++
		}
		if types.IsAvailableLike(result) {
			stats.Available++
		}
		if types.IsAvailableSoon(result) {
			stats.AvailableSoon++
		}
		if record.LastCheckedAt.After(stats.LastCheckedAt) {
			stats.LastCheckedAt = record.LastCheckedAt
		}
	}
	return stats
}

// Dirty reports whether there are unsaved changes.
func (s *Store) Dirty() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.dirty
}

// Save atomically persists the database (temp file + rename).
func (s *Store) Save() error {
	if s == nil || !s.enabled || s.path == "" {
		return nil
	}
	s.mu.Lock()
	data, err := json.MarshalIndent(diskFormat{Version: 1, Records: s.records}, "", "  ")
	s.mu.Unlock()
	if err != nil {
		return fmt.Errorf("encode store: %w", err)
	}

	if err := os.MkdirAll(filepath.Dir(s.path), 0o755); err != nil {
		return fmt.Errorf("create store directory: %w", err)
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return fmt.Errorf("write store: %w", err)
	}
	if err := os.Rename(tmp, s.path); err != nil {
		return fmt.Errorf("replace store: %w", err)
	}

	s.mu.Lock()
	s.dirty = false
	s.mu.Unlock()
	return nil
}
