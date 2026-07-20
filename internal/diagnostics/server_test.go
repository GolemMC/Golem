// SPDX-License-Identifier: AGPL-3.0-only

package diagnostics

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/GolemMC/Golem/internal/config"
)

type source struct{ snapshot Snapshot }

func (s source) DiagnosticSnapshot() Snapshot { return s.snapshot }

func TestBearerTokenProtectsOperationalEndpoints(t *testing.T) {
	server := New(structConfig("secret"), source{}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	handler := server.auth(server.ready)
	request := httptest.NewRequest(http.MethodGet, "/ready", nil)
	response := httptest.NewRecorder()
	handler(response, request)
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("got %d", response.Code)
	}
	request = httptest.NewRequest(http.MethodGet, "/ready", nil)
	request.Header.Set("Authorization", "Bearer secret")
	response = httptest.NewRecorder()
	handler(response, request)
	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("authenticated readiness got %d", response.Code)
	}
}

func TestHealthIsMinimalAndUnauthenticated(t *testing.T) {
	server := New(structConfig("secret"), source{snapshot: Snapshot{Players: 10, AuthenticationHealthy: true}}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	request := httptest.NewRequest(http.MethodGet, "/health", nil)
	response := httptest.NewRecorder()
	server.health(response, request)
	if response.Code != http.StatusOK || strings.TrimSpace(response.Body.String()) != `{"status":"healthy"}` {
		t.Fatalf("unexpected health response %d %q", response.Code, response.Body.String())
	}
}

func TestMetricsIncludesPendingChunkLoads(t *testing.T) {
	server := New(structConfig(""), source{snapshot: Snapshot{PendingChunkLoads: 23}}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	request := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	response := httptest.NewRecorder()
	server.metrics(response, request)
	if !strings.Contains(response.Body.String(), "golem_chunk_loads_pending 23\n") {
		t.Fatalf("pending metric missing from %q", response.Body.String())
	}
}

func structConfig(token string) config.Diagnostics {
	return config.Diagnostics{BearerToken: token}
}
