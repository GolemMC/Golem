// SPDX-License-Identifier: AGPL-3.0-only

package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestFirstLaunchGeneratesSecureConfigAndContinues(t *testing.T) {
	directory := t.TempDir()
	if err := os.Mkdir(filepath.Join(directory, "world"), 0o750); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(directory, DefaultPath)
	cfg, result, err := LoadOrCreate(path, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !result.Generated || cfg.Diagnostics.BearerToken == "" {
		t.Fatalf("generated=%v token-empty=%v", result.Generated, cfg.Diagnostics.BearerToken == "")
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("mode=%04o, want 0600", got)
	}
}

func TestFirstLaunchStillFailsClearlyWhenWorldIsMissing(t *testing.T) {
	path := filepath.Join(t.TempDir(), DefaultPath)
	_, result, err := LoadOrCreate(path, nil)
	if !result.Generated {
		t.Fatal("configuration was not generated")
	}
	if err == nil || !strings.Contains(err.Error(), "world.path") || !strings.Contains(err.Error(), "no such file") {
		t.Fatalf("expected clear missing-world error, got %v", err)
	}
	if _, statErr := os.Stat(path); statErr != nil {
		t.Fatalf("generated configuration was not retained: %v", statErr)
	}
}

func TestBroadConfigurationPermissionsProduceWarning(t *testing.T) {
	directory := t.TempDir()
	if err := os.Mkdir(filepath.Join(directory, "world"), 0o750); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(directory, DefaultPath)
	data := "[world]\npath = \"./world\"\nbackup_directory = \"./backups\"\n[diagnostics]\naddress = \"127.0.0.1\"\n"
	if err := os.WriteFile(path, []byte(data), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}
	_, result, err := LoadOrCreate(path, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Warnings) == 0 || !strings.Contains(strings.Join(result.Warnings, "\n"), "permissions 0644") {
		t.Fatalf("permission warning missing: %v", result.Warnings)
	}
}

func TestLoadRejectsUnknownTOML(t *testing.T) {
	directory := t.TempDir()
	worldPath := filepath.Join(directory, "world")
	if err := os.Mkdir(worldPath, 0o750); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(directory, DefaultPath)
	data := "[world]\npath = \"./world\"\nbackup_directory = \"./backups\"\n[server]\nunknown = true\n"
	if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
	_, _, err := LoadOrCreate(path, nil)
	if err == nil || !strings.Contains(err.Error(), "strict mode") {
		t.Fatalf("expected unknown-field error, got %v", err)
	}
}

func TestEnvironmentAndCLIOverridePrecedence(t *testing.T) {
	directory := t.TempDir()
	if err := os.Mkdir(filepath.Join(directory, "world"), 0o750); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(directory, DefaultPath)
	data := "[server]\nport = 20000\n[world]\npath = \"./world\"\nbackup_directory = \"./backups\"\n[diagnostics]\naddress = \"127.0.0.1\"\n"
	if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, result, err := LoadOrCreate(path, []string{"GOLEM_SERVER_PORT=20001"})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Server.Port != 20001 {
		t.Fatalf("environment port=%d", cfg.Server.Port)
	}
	maxPlayers := 12
	if _, err := (Overrides{Listen: "127.0.0.1:20002", MaxPlayers: &maxPlayers}).Apply(&cfg, filepath.Dir(result.Path)); err != nil {
		t.Fatal(err)
	}
	if cfg.Server.Port != 20002 || cfg.Server.MaxPlayers != 12 {
		t.Fatalf("CLI overrides not applied: %+v", cfg.Server)
	}
}

func TestParseBytes(t *testing.T) {
	for input, expected := range map[string]int64{"0": 0, "512MiB": 512 << 20, "4GiB": 4 << 30, "100MB": 100_000_000} {
		value, err := ParseBytes(input)
		if err != nil || value != expected {
			t.Fatalf("ParseBytes(%q)=(%d,%v), want %d", input, value, err, expected)
		}
	}
	if _, err := ParseBytes("lots"); err == nil {
		t.Fatal("invalid size accepted")
	}
}

func TestValidationKeepsBackupOutsideWorldAndOnlineMode(t *testing.T) {
	cfg := Defaults()
	cfg.World.Path = t.TempDir()
	cfg.World.BackupDirectory = filepath.Join(cfg.World.Path, "backups")
	cfg.Auth.OnlineMode = false
	_, err := cfg.Validate()
	if err == nil || !strings.Contains(err.Error(), "outside") || !strings.Contains(err.Error(), "online_mode") {
		t.Fatalf("expected safety errors, got %v", err)
	}
}
