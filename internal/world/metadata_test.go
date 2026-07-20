// SPDX-License-Identifier: AGPL-3.0-only

package world

import (
	"compress/gzip"
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	"github.com/GolemMC/Golem/internal/config"
	"github.com/GolemMC/Golem/internal/version"
	"github.com/Tnze/go-mc/nbt"
)

func writeLevel(t *testing.T, dir string, dataVersion int32) {
	t.Helper()
	f, err := os.Create(filepath.Join(dir, "level.dat"))
	if err != nil {
		t.Fatal(err)
	}
	gz := gzip.NewWriter(f)
	root := struct {
		Data struct {
			DataVersion int32  `nbt:"DataVersion"`
			LevelName   string `nbt:"LevelName"`
			SpawnX      int32  `nbt:"SpawnX"`
			SpawnY      int32  `nbt:"SpawnY"`
			SpawnZ      int32  `nbt:"SpawnZ"`
		} `nbt:"Data"`
	}{}
	root.Data.DataVersion = dataVersion
	root.Data.LevelName = "Test"
	root.Data.SpawnY = 64
	if err := nbt.NewEncoder(gz).Encode(root, ""); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestMetadataVersion(t *testing.T) {
	dir := t.TempDir()
	writeLevel(t, dir, version.WorldDataVersion)
	m, err := ReadMetadata(filepath.Join(dir, "level.dat"))
	if err != nil {
		t.Fatal(err)
	}
	if m.DataVersion != version.WorldDataVersion || m.Spawn.Y != 64 {
		t.Fatalf("got %+v", m)
	}
	if err := ValidateMetadata(m); err != nil {
		t.Fatal(err)
	}
	m.DataVersion--
	if err := ValidateMetadata(m); err == nil {
		t.Fatal("expected version error")
	}
}

func TestLockExclusive(t *testing.T) {
	dir := t.TempDir()
	one, err := AcquireLock(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer one.Close()
	if _, err := AcquireLock(dir); err == nil {
		t.Fatal("second lock unexpectedly succeeded")
	}
}

func TestOpenBacksUpAndReleasesLock(t *testing.T) {
	dir := t.TempDir()
	worldPath := filepath.Join(dir, "world")
	if err := os.Mkdir(worldPath, 0o750); err != nil {
		t.Fatal(err)
	}
	writeLevel(t, worldPath, version.WorldDataVersion)
	cfg := config.Defaults().World
	cfg.Path = worldPath
	cfg.BackupDirectory = filepath.Join(dir, "backups")
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	w, err := Open(cfg, log)
	if err != nil {
		t.Fatal(err)
	}
	if !w.LockHeld() {
		t.Fatal("world lock not held")
	}
	if _, err := os.Stat(w.BackupPath); err != nil {
		t.Fatalf("backup missing: %v", err)
	}
	if _, err := Open(cfg, log); err == nil {
		t.Fatal("second world open unexpectedly succeeded")
	}
	if err := w.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	w, err = Open(cfg, log)
	if err != nil {
		t.Fatalf("reopen after clean close: %v", err)
	}
	if err := w.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestUnsupportedWorldIsNotTouched(t *testing.T) {
	dir := t.TempDir()
	worldPath := filepath.Join(dir, "world")
	if err := os.Mkdir(worldPath, 0o750); err != nil {
		t.Fatal(err)
	}
	writeLevel(t, worldPath, version.WorldDataVersion-1)
	cfg := config.Defaults().World
	cfg.Path = worldPath
	cfg.BackupDirectory = filepath.Join(dir, "backups")
	_, err := Open(cfg, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err == nil {
		t.Fatal("unsupported world unexpectedly opened")
	}
	if _, err := os.Stat(filepath.Join(worldPath, "session.lock")); !os.IsNotExist(err) {
		t.Fatalf("world was touched: %v", err)
	}
	if _, err := os.Stat(cfg.BackupDirectory); !os.IsNotExist(err) {
		t.Fatalf("backup directory was created: %v", err)
	}
}
