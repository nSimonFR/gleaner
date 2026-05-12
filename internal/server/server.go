// Package server implements the SPEC §13.7 HTTP surface — JSON at
// /api/v1/* and an HTML dashboard at /. The server binds 127.0.0.1
// only (Tailscale Serve proxies from there in nic-os deployments;
// matches every other gleaner-adjacent service on the rpi5).
//
// State is read live each request from the orchestrator's *State,
// CodeHost (for inflight + merged-this-week + daily counts), and the
// QuotaSources (for the per-provider quota block). The handlers are
// stateless — restart is free.
package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/nSimonFR/gleaner/internal/adapter"
	codehost "github.com/nSimonFR/gleaner/internal/adapter/codehost/github"
	"github.com/nSimonFR/gleaner/internal/adapter/tracker"
	"github.com/nSimonFR/gleaner/internal/config"
	"github.com/nSimonFR/gleaner/internal/logging"
	"github.com/nSimonFR/gleaner/internal/orchestrator"
)

// Server bundles every dependency a handler needs. Constructed once at
// startup; goroutine-safe.
type Server struct {
	Cfg           *config.Config
	Tracker       tracker.Tracker
	CodeHost      *codehost.Client
	CodehostRepos []string
	QuotaSources  map[string]adapter.QuotaSource
	State         *orchestrator.State

	// Refresh is the channel the POST /api/v1/refresh handler signals.
	// The orchestrator's tick loop listens on this channel; a buffered
	// size-1 channel + select-default coalesces bursts (SPEC §13.7
	// "implementations MAY coalesce repeated requests").
	Refresh chan struct{}

	refreshMu sync.Mutex
	lastFired time.Time
}

// Start binds the configured port on 127.0.0.1 and serves. Returns the
// *http.Server so the caller can Shutdown it. If port <= 0 it's a no-op
// (returns nil — gleaner runs headless when server.port is unset).
func (s *Server) Start(ctx context.Context) (*http.Server, error) {
	if s.Cfg.Server.Port <= 0 {
		return nil, nil
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/", s.handleDashboard)
	mux.HandleFunc("/healthz", s.handleHealthz)
	mux.HandleFunc("/api/v1/state", s.handleState)
	mux.HandleFunc("/api/v1/refresh", s.handleRefresh)
	mux.HandleFunc("/api/v1/", s.handleIssue) // matches /api/v1/<identifier>

	srv := &http.Server{
		Addr:         fmt.Sprintf("127.0.0.1:%d", s.Cfg.Server.Port),
		Handler:      mux,
		ReadTimeout:  10 * time.Second,
		WriteTimeout: 10 * time.Second,
	}
	logging.Log("http_server_start",
		logging.F("addr", srv.Addr))
	go func() {
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			logging.Log("http_server_error", logging.F("err", err))
		}
	}()
	return srv, nil
}

// handleState serves SPEC §13.7.2 — the orchestrator state snapshot.
func (s *Server) handleState(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	snap := s.BuildSnapshot(r.Context())
	writeJSON(w, http.StatusOK, snap)
}

// handleRefresh accepts a "fire the next tick now" signal. SPEC §13.7
// "POST /api/v1/refresh" — returns 202 with `{queued, coalesced}`.
func (s *Server) handleRefresh(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	coalesced := false
	select {
	case s.Refresh <- struct{}{}:
		// queued
	default:
		coalesced = true
	}
	s.refreshMu.Lock()
	requested := time.Now()
	s.lastFired = requested
	s.refreshMu.Unlock()

	resp := map[string]any{
		"queued":       true,
		"coalesced":    coalesced,
		"requested_at": requested.UTC().Format(time.RFC3339),
		"operations":   []string{"poll", "reconcile"},
	}
	writeJSON(w, http.StatusAccepted, resp)
}

// handleIssue serves SPEC §13.7 per-issue details — /api/v1/<id>.
// Identifier is the tail of the URL after /api/v1/. The orchestrator's
// in-memory state is the source of truth; if the issue is neither
// running nor retrying, return 404.
func (s *Server) handleIssue(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	id := strings.TrimPrefix(r.URL.Path, "/api/v1/")
	if id == "" || id == "state" || id == "refresh" {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	// `id` may be URL-encoded ("nSimonFR%2Fnic-os%2360"). Decode it.
	decoded, err := decodePath(id)
	if err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}

	for _, w0 := range s.State.SnapshotRunning() {
		if w0.Issue.Identifier == decoded || w0.Issue.ID == decoded {
			writeJSON(w, http.StatusOK, issueRunningJSON(w0))
			return
		}
	}
	for _, r0 := range s.State.SnapshotRetries() {
		if r0.Issue.Identifier == decoded || r0.Issue.ID == decoded {
			writeJSON(w, http.StatusOK, issueRetryJSON(r0))
			return
		}
	}
	http.Error(w, "not found", http.StatusNotFound)
}

// handleHealthz returns "ok" for liveness probes (Tailscale Serve
// monitor, rpi5 Beszel checks). No state lookups; just confirms the
// HTTP path is alive.
func (s *Server) handleHealthz(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("ok\n"))
}

// handleDashboard serves the human-readable HTML at /. Minimal — no
// JS, no CSS framework. The homepage widget in nic-os reads
// /api/v1/state directly; this is for ad-hoc browser inspection.
func (s *Server) handleDashboard(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	snap := s.BuildSnapshot(r.Context())
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	renderDashboard(w, snap)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// decodePath is a wrapper around url.PathUnescape that handles the
// nuance that GitHub identifiers contain "/" — operators query
// `/api/v1/nSimonFR%2Fnic-os%2360` and the handler sees
// `nSimonFR/nic-os#60` after decoding.
func decodePath(s string) (string, error) {
	// net/url.PathUnescape handles %XX → bytes.
	return urlPathUnescape(s)
}
