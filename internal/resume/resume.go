// Package resume persists scan progress so an interrupted scan can be
// resumed later with `qda run -resume`.
package resume

import (
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// State is the on-disk scan state.
type State struct {
	Version     int       `json:"version"`
	ScanID      string    `json:"scan_id"`
	InputHash   string    `json:"input_hash,omitempty"`
	TLDs        []string  `json:"tlds,omitempty"`
	Total       int       `json:"total"`
	Completed   int       `json:"completed"`
	Pending     []string  `json:"pending"`
	StartedAt   time.Time  `json:"started_at"`
	UpdatedAt   time.Time  `json:"updated_at"`
	FinishedAt  *time.Time `json:"finished_at,omitempty"`
}

// Manager keeps track of pending domains and saves them atomically.
type Manager struct {
	mu    sync.Mutex
	path  string
	state State
}

// HashTargets fingerprints a target list for resume validation.
func HashTargets(domains []string) string {
	sorted := append([]string(nil), domains...)
	sort.Strings(sorted)
	hash := sha1.Sum([]byte(strings.Join(sorted, "\n")))
	return hex.EncodeToString(hash[:])
}

// New creates a fresh state for a new scan.
func New(path string, inputHash string, tlds []string, pending []string) *Manager {
	now := time.Now().UTC()
	return &Manager{
		path: path,
		state: State{
			Version:   1,
			ScanID:    now.Format("20060102-150405"),
			InputHash: inputHash,
			TLDs:      append([]string(nil), tlds...),
			Total:     len(pending),
			Pending:   append([]string(nil), pending...),
			StartedAt: now,
			UpdatedAt: now,
		},
	}
}

// Load reads an existing state file.
func Load(path string) (*Manager, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("no saved scan state at %s (run a scan first, it is saved automatically)", path)
		}
		return nil, fmt.Errorf("read resume state: %w", err)
	}
	var state State
	if err := json.Unmarshal(data, &state); err != nil {
		return nil, fmt.Errorf("parse resume state %s: %w", path, err)
	}
	if state.Version != 1 {
		return nil, fmt.Errorf("unsupported resume state version %d", state.Version)
	}
	return &Manager{path: path, state: state}, nil
}

// Exists reports whether a state file exists.
func Exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// State returns a copy of the current state.
func (m *Manager) State() State {
	m.mu.Lock()
	defer m.mu.Unlock()
	state := m.state
	state.Pending = append([]string(nil), m.state.Pending...)
	state.TLDs = append([]string(nil), m.state.TLDs...)
	return state
}

// Path returns the state file path.
func (m *Manager) Path() string { return m.path }

// Complete marks one domain as done.
func (m *Manager) Complete(domain string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for i, pending := range m.state.Pending {
		if pending == domain {
			m.state.Pending = append(m.state.Pending[:i], m.state.Pending[i+1:]...)
			break
		}
	}
	m.state.Completed = m.state.Total - len(m.state.Pending)
	if m.state.Completed < 0 {
		m.state.Completed = 0
	}
	m.state.UpdatedAt = time.Now().UTC()
}

// SetPending replaces the pending list (used on graceful shutdown with the
// exact remaining queue).
func (m *Manager) SetPending(pending []string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.state.Pending = append([]string(nil), pending...)
	m.state.Completed = m.state.Total - len(pending)
	if m.state.Completed < 0 {
		m.state.Completed = 0
	}
	m.state.UpdatedAt = time.Now().UTC()
}

// MarkFinished stamps the state as finished.
func (m *Manager) MarkFinished() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.state.Pending = nil
	m.state.Completed = m.state.Total
	now := time.Now().UTC()
	m.state.FinishedAt = &now
	m.state.UpdatedAt = now
}

// Finished reports whether the scan was completed.
func (m *Manager) Finished() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.state.FinishedAt != nil || (m.state.Total > 0 && len(m.state.Pending) == 0)
}

// Save atomically writes the state file.
func (m *Manager) Save() error {
	if strings.TrimSpace(m.path) == "" {
		return errors.New("resume state path is empty")
	}
	m.mu.Lock()
	data, err := json.MarshalIndent(m.state, "", "  ")
	m.mu.Unlock()
	if err != nil {
		return fmt.Errorf("encode resume state: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(m.path), 0o755); err != nil {
		return fmt.Errorf("create resume state directory: %w", err)
	}
	tmp := m.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return fmt.Errorf("write resume state: %w", err)
	}
	if err := os.Rename(tmp, m.path); err != nil {
		return fmt.Errorf("replace resume state: %w", err)
	}
	return nil
}

// Clear removes the state file.
func (m *Manager) Clear() error {
	if strings.TrimSpace(m.path) == "" {
		return nil
	}
	if err := os.Remove(m.path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}
