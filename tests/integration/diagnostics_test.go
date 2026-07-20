// SPDX-License-Identifier: AGPL-3.0-only

package integration

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"syscall"
	"testing"
	"time"

	"github.com/GolemMC/Golem/internal/config"
	"github.com/GolemMC/Golem/internal/diagnostics"
)

type diagnosticSource struct{}

func (diagnosticSource) DiagnosticSnapshot() diagnostics.Snapshot {
	return diagnostics.Snapshot{WorldLockHeld: true}
}

func TestDiagnosticsHTTPBoundary(t *testing.T) {
	server := diagnostics.New(config.Diagnostics{Address: "127.0.0.1", Port: 0, BearerToken: "integration-secret"}, diagnosticSource{}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err := server.Start(); err != nil {
		if errors.Is(err, syscall.EPERM) {
			t.Skip("sandbox does not permit loopback listeners")
		}
		t.Fatal(err)
	}
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = server.Shutdown(ctx)
	}()
	base := "http://" + server.Addr().String()
	response, err := http.Get(base + "/health")
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	var health map[string]string
	if err := json.NewDecoder(response.Body).Decode(&health); err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusOK || len(health) != 1 || health["status"] != "healthy" {
		t.Fatalf("health status=%d body=%v", response.StatusCode, health)
	}
	request, _ := http.NewRequest(http.MethodGet, base+"/debug/status", nil)
	response, err = http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	if response.StatusCode != http.StatusUnauthorized {
		t.Fatalf("unauthenticated status code=%d", response.StatusCode)
	}
	request, _ = http.NewRequest(http.MethodGet, base+"/ready", nil)
	request.Header.Set("Authorization", "Bearer integration-secret")
	response, err = http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("authenticated readiness code=%d", response.StatusCode)
	}
}
