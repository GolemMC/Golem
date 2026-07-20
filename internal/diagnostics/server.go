// SPDX-License-Identifier: AGPL-3.0-only

// Package diagnostics exposes read-only health and operational metrics. It
// depends only on immutable snapshots, never mutable game internals.
package diagnostics

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/GolemMC/Golem/internal/config"
	"github.com/GolemMC/Golem/internal/world"
)

type Snapshot struct {
	Uptime                time.Duration   `json:"uptime"`
	Players               int             `json:"players"`
	MaxPlayers            int             `json:"max_players"`
	LoadedChunks          int             `json:"loaded_chunks"`
	PendingChunkLoads     int             `json:"pending_chunk_loads"`
	Tick                  TickSnapshot    `json:"tick"`
	Save                  world.SaveState `json:"save"`
	WorldLockHeld         bool            `json:"world_lock_held"`
	AuthenticationHealthy bool            `json:"authentication_healthy"`
	MemoryBytes           uint64          `json:"memory_bytes"`
	Goroutines            int             `json:"goroutines"`
}

type TickSnapshot struct {
	Last     time.Duration `json:"last"`
	Average  time.Duration `json:"average"`
	TPS      float64       `json:"tps"`
	Overruns uint64        `json:"overruns"`
	Ticks    uint64        `json:"ticks"`
}
type Source interface{ DiagnosticSnapshot() Snapshot }

type Server struct {
	cfg      config.Diagnostics
	source   Source
	log      *slog.Logger
	http     *http.Server
	listener net.Listener
}

func New(cfg config.Diagnostics, source Source, log *slog.Logger) *Server {
	return &Server{cfg: cfg, source: source, log: log}
}

func (s *Server) Start() error {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", s.health)
	mux.HandleFunc("GET /ready", s.auth(s.ready))
	mux.HandleFunc("GET /metrics", s.auth(s.metrics))
	mux.HandleFunc("GET /debug/status", s.auth(s.status))
	s.http = &http.Server{Addr: net.JoinHostPort(s.cfg.Address, strconv.Itoa(s.cfg.Port)), Handler: http.MaxBytesHandler(mux, 1024), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 5 * time.Second, WriteTimeout: 10 * time.Second, IdleTimeout: 30 * time.Second, MaxHeaderBytes: 8 << 10}
	ln, err := net.Listen("tcp", s.http.Addr)
	if err != nil {
		return fmt.Errorf("listen for diagnostics: %w", err)
	}
	s.listener = ln
	go func() {
		if err := s.http.Serve(ln); err != nil && err != http.ErrServerClosed {
			s.log.Error("diagnostics server failed", "error", err)
		}
	}()
	return nil
}

func (s *Server) Addr() net.Addr {
	if s.listener == nil {
		return nil
	}
	return s.listener.Addr()
}
func (s *Server) Shutdown(ctx context.Context) error {
	if s.http == nil {
		return nil
	}
	return s.http.Shutdown(ctx)
}

func (s *Server) auth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		if s.cfg.BearerToken != "" {
			got := r.Header.Get("Authorization")
			want := "Bearer " + s.cfg.BearerToken
			if len(got) != len(want) || subtle.ConstantTimeCompare([]byte(got), []byte(want)) != 1 {
				w.Header().Set("WWW-Authenticate", "Bearer")
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
		}
		next(w, r)
	}
}

func (s *Server) health(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, map[string]string{"status": "healthy"})
}
func (s *Server) ready(w http.ResponseWriter, _ *http.Request) {
	snap := s.source.DiagnosticSnapshot()
	code := http.StatusOK
	state := "ready"
	if !snap.WorldLockHeld || snap.Save.Phase == world.SaveFailed {
		code = http.StatusServiceUnavailable
		state = "not_ready"
	}
	writeJSON(w, code, map[string]string{"status": state})
}
func (s *Server) status(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, s.source.DiagnosticSnapshot())
}
func (s *Server) metrics(w http.ResponseWriter, _ *http.Request) {
	v := s.source.DiagnosticSnapshot()
	w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
	fmt.Fprintf(w, "golem_uptime_seconds %.3f\ngolem_players %d\ngolem_players_max %d\ngolem_chunks_loaded %d\ngolem_chunk_loads_pending %d\ngolem_chunks_dirty %d\ngolem_tick_duration_seconds %.6f\ngolem_tick_overruns_total %d\ngolem_tps %.3f\ngolem_memory_bytes %d\ngolem_goroutines %d\ngolem_world_lock_held %d\ngolem_authentication_healthy %d\n", v.Uptime.Seconds(), v.Players, v.MaxPlayers, v.LoadedChunks, v.PendingChunkLoads, v.Save.DirtyChunks, v.Tick.Last.Seconds(), v.Tick.Overruns, v.Tick.TPS, v.MemoryBytes, v.Goroutines, boolInt(v.WorldLockHeld), boolInt(v.AuthenticationHealthy))
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
func boolInt(v bool) int {
	if v {
		return 1
	}
	return 0
}

func RuntimeFields(s Snapshot) Snapshot {
	var m runtime.MemStats
	runtime.ReadMemStats(&m)
	s.MemoryBytes = m.Alloc
	s.Goroutines = runtime.NumGoroutine()
	return s
}
func ExposedWithoutToken(cfg config.Diagnostics) bool {
	return cfg.Enabled && (cfg.Address == "0.0.0.0" || cfg.Address == "::" || strings.TrimSpace(cfg.Address) == "") && cfg.BearerToken == ""
}
