// Package api implements the qda HTTP server mode.
package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"qda/internal/domainx"
	"qda/internal/output"
	"qda/internal/runner"
	"qda/internal/store"
	"qda/internal/types"
)

// Server exposes the engine and the local database over HTTP.
type Server struct {
	engine           *runner.Engine
	store            *store.Store
	printer          *output.Printer
	token            string
	expiringSoonDays int
	mux              *http.ServeMux
}

// NewServer builds the API server. token is optional; when set, requests
// must carry Authorization: Bearer <token> or X-API-Key: <token>.
func NewServer(engine *runner.Engine, db *store.Store, printer *output.Printer, token string, expiringSoonDays int) *Server {
	s := &Server{engine: engine, store: db, printer: printer, token: token, expiringSoonDays: expiringSoonDays}
	s.mux = http.NewServeMux()
	s.mux.HandleFunc("GET /health", s.handleHealth)
	s.mux.HandleFunc("GET /v1/stats", s.handleStats)
	s.mux.HandleFunc("GET /v1/domains", s.handleDomains)
	s.mux.HandleFunc("GET /v1/domains/{domain}", s.handleDomain)
	s.mux.HandleFunc("GET /v1/check", s.handleCheck)
	s.mux.HandleFunc("POST /v1/check", s.handleCheckBatch)
	return s
}

// Handler returns the root handler with auth middleware.
func (s *Server) Handler() http.Handler {
	return s.auth(s.mux)
}

// ListenAndServe starts the HTTP server and blocks until ctx is cancelled.
func (s *Server) ListenAndServe(ctx context.Context, listen string) error {
	listener, err := net.Listen("tcp", listen)
	if err != nil {
		return fmt.Errorf("listen %s: %w", listen, err)
	}
	server := &http.Server{
		Handler:           s.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
	}
	errCh := make(chan error, 1)
	go func() {
		s.printer.Infof("api server listening on http://%s", listener.Addr().String())
		errCh <- server.Serve(listener)
	}()

	select {
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdownCtx)
		return nil
	case err := <-errCh:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	}
}

func (s *Server) auth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s.token == "" || r.URL.Path == "/health" {
			next.ServeHTTP(w, r)
			return
		}
		token := r.Header.Get("X-API-Key")
		if token == "" {
			if value := r.Header.Get("Authorization"); strings.HasPrefix(value, "Bearer ") {
				token = strings.TrimPrefix(value, "Bearer ")
			}
		}
		if token != s.token {
			writeError(w, http.StatusUnauthorized, "missing or invalid credentials")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) handleStats(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.store.Stats())
}

func (s *Server) handleDomains(w http.ResponseWriter, r *http.Request) {
	query := store.Query{
		Status:   r.URL.Query().Get("status"),
		TLD:      r.URL.Query().Get("tld"),
		Contains: r.URL.Query().Get("q"),
	}
	if value := r.URL.Query().Get("expiring_in"); value != "" {
		days, err := strconv.Atoi(value)
		if err != nil || days < 0 {
			writeError(w, http.StatusBadRequest, "expiring_in must be a non-negative integer (days)")
			return
		}
		query.ExpiringIn = &days
	}
	if value := r.URL.Query().Get("available"); value == "true" || value == "1" {
		query.AvailableSoon = true
	}

	records := s.store.Query(query)
	if value := r.URL.Query().Get("limit"); value != "" {
		limit, err := strconv.Atoi(value)
		if err != nil || limit < 0 {
			writeError(w, http.StatusBadRequest, "limit must be a non-negative integer")
			return
		}
		if limit > 0 && len(records) > limit {
			records = records[:limit]
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"count":   len(records),
		"records": records,
	})
}

func (s *Server) handleDomain(w http.ResponseWriter, r *http.Request) {
	domain := strings.ToLower(r.PathValue("domain"))
	record, ok := s.store.Get(domain)
	if !ok {
		writeError(w, http.StatusNotFound, "domain not found in the local database")
		return
	}
	writeJSON(w, http.StatusOK, record)
}

func (s *Server) handleCheck(w http.ResponseWriter, r *http.Request) {
	domain := r.URL.Query().Get("domain")
	if domain == "" {
		writeError(w, http.StatusBadRequest, "domain query parameter is required")
		return
	}
	normalized, err := domainx.RegistrableDomain(domain)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid domain: "+err.Error())
		return
	}

	force := r.URL.Query().Get("force") == "true" || r.URL.Query().Get("force") == "1"
	if !force {
		if record, ok := s.store.Reusable(normalized, time.Now().UTC()); ok {
			writeJSON(w, http.StatusOK, store.ToResult(record, s.expiringSoonDays, time.Now().UTC()))
			return
		}
	}

	result := s.engine.CheckDomain(r.Context(), normalized)
	s.store.Put(result)
	runner.SaveStore(s.store, s.printer)
	writeJSON(w, http.StatusOK, result)
}

type batchRequest struct {
	Domains []string `json:"domains"`
	Words   []string `json:"words"`
	TLDs    []string `json:"tlds"`
}

func (s *Server) handleCheckBatch(w http.ResponseWriter, r *http.Request) {
	var request batchRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<20)).Decode(&request); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body: "+err.Error())
		return
	}
	if len(request.Domains) == 0 && len(request.Words) == 0 {
		writeError(w, http.StatusBadRequest, "provide domains and/or words")
		return
	}

	var domains []string
	seen := map[string]bool{}
	for _, value := range request.Domains {
		normalized, err := domainx.RegistrableDomain(value)
		if err != nil {
			continue
		}
		if !seen[normalized] {
			seen[normalized] = true
			domains = append(domains, normalized)
		}
	}
	tlds := request.TLDs
	if len(tlds) == 0 {
		tlds = []string{"com"}
	}
	for _, word := range request.Words {
		normalizedWord, err := domainx.NormalizeWord(word)
		if err != nil {
			continue
		}
		for _, tld := range tlds {
			domain, err := domainx.BuildDomainFromWord(normalizedWord, tld)
			if err != nil || seen[domain] {
				continue
			}
			seen[domain] = true
			domains = append(domains, domain)
		}
	}
	if len(domains) == 0 {
		writeError(w, http.StatusBadRequest, "no valid domains to check")
		return
	}
	if len(domains) > 1000 {
		writeError(w, http.StatusBadRequest, "batch too large: maximum 1000 domains per request")
		return
	}

	results := make([]types.Result, len(domains))
	var wg sync.WaitGroup
	semaphore := make(chan struct{}, 4)
	for i, domain := range domains {
		wg.Add(1)
		go func(i int, domain string) {
			defer wg.Done()
			semaphore <- struct{}{}
			defer func() { <-semaphore }()
			if r.Context().Err() != nil {
				return
			}
			result := s.engine.CheckDomain(r.Context(), domain)
			s.store.Put(result)
			results[i] = result
		}(i, domain)
	}
	wg.Wait()
	runner.SaveStore(s.store, s.printer)

	if r.Context().Err() != nil {
		writeError(w, http.StatusRequestTimeout, "request cancelled")
		return
	}
	// Keep response ordering deterministic (available first, then alpha).
	sort.SliceStable(results, func(i, j int) bool {
		left := types.SortRank(results[i])
		right := types.SortRank(results[j])
		if left != right {
			return left < right
		}
		return results[i].Domain < results[j].Domain
	})
	writeJSON(w, http.StatusOK, map[string]any{
		"count":   len(results),
		"results": results,
	})
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	encoder := json.NewEncoder(w)
	encoder.SetIndent("", "  ")
	_ = encoder.Encode(value)
}

func writeError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]string{"error": message})
}
