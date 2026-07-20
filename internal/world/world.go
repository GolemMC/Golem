// SPDX-License-Identifier: AGPL-3.0-only

// Package world owns Minecraft world validation, locking, backups, Anvil I/O,
// player data, and save coordination. It has no network dependencies.
package world

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/GolemMC/Golem/internal/config"
	"github.com/GolemMC/Golem/internal/world/region"
)

type SavePhase string

const (
	SaveIdle   SavePhase = "idle"
	SaveSaving SavePhase = "saving"
	SaveFailed SavePhase = "failed"
)

type SaveState struct {
	Phase       SavePhase `json:"phase"`
	LastSuccess time.Time `json:"last_success,omitempty"`
	LastError   string    `json:"last_error,omitempty"`
	DirtyChunks int       `json:"dirty_chunks"`
}

type World struct {
	Path         string
	Metadata     Metadata
	BackupPath   string
	lock         *Lock
	mu           sync.RWMutex
	save         SaveState
	loadedChunks int
	loaded       map[[2]int32]struct{}
	log          *slog.Logger
	editMu       sync.Mutex
	regions      *region.Store
}

func Open(cfg config.World, log *slog.Logger) (*World, error) {
	levelPath := filepath.Join(cfg.Path, "level.dat")
	meta, err := ReadMetadata(levelPath)
	if err != nil {
		return nil, err
	}
	if err := ValidateMetadata(meta); err != nil {
		return nil, err
	}
	log.Info("world metadata validated", "data_version", meta.DataVersion)
	lock, err := AcquireLock(cfg.Path)
	if err != nil {
		return nil, err
	}
	log.Info("world lock acquired")
	w := &World{Path: cfg.Path, Metadata: meta, lock: lock, save: SaveState{Phase: SaveIdle}, loaded: make(map[[2]int32]struct{}), log: log, regions: region.NewStore()}
	backup, err := CreateBackup(cfg.Path, cfg.BackupDirectory, cfg.BackupRetention, time.Now())
	if err != nil {
		_ = lock.Close()
		if cfg.RequireBackup {
			return nil, fmt.Errorf("mandatory startup backup failed; world was not opened: %w", err)
		}
		log.Error("optional startup backup failed", "error", err)
	} else {
		w.BackupPath = backup
		log.Info("startup world backup created", "path", backup)
	}
	return w, nil
}

func (w *World) LockHeld() bool       { return w != nil && w.lock != nil && w.lock.Held() }
func (w *World) LoadedChunks() int    { w.mu.RLock(); defer w.mu.RUnlock(); return w.loadedChunks }
func (w *World) SaveState() SaveState { w.mu.RLock(); defer w.mu.RUnlock(); return w.save }

func (w *World) Save(ctx context.Context) error {
	w.mu.Lock()
	w.save.Phase = SaveSaving
	w.save.LastError = ""
	w.mu.Unlock()
	select {
	case <-ctx.Done():
		w.recordSaveError(ctx.Err())
		return ctx.Err()
	default:
	}
	// The first rebuilt milestone streams chunks read-only. Later gameplay
	// writers will register dirty work here before save coordination expands.
	w.mu.Lock()
	w.save.Phase = SaveIdle
	w.save.LastSuccess = time.Now()
	w.mu.Unlock()
	w.log.Debug("world save state recorded")
	return nil
}

func (w *World) recordSaveError(err error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.save.Phase = SaveFailed
	w.save.LastError = err.Error()
}

func (w *World) Close(ctx context.Context) error {
	if w == nil {
		return nil
	}
	var saveErr error
	if err := w.Save(ctx); err != nil {
		saveErr = fmt.Errorf("final world save: %w", err)
	}
	var lockErr error
	if w.lock != nil {
		lockErr = w.lock.Close()
	}
	if saveErr != nil {
		return saveErr
	}
	return lockErr
}

func AtomicWriteFile(path string, data []byte, mode os.FileMode) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return fmt.Errorf("create parent directory: %w", err)
	}
	f, err := os.CreateTemp(dir, ".golem-save-*.tmp")
	if err != nil {
		return err
	}
	tmp := f.Name()
	ok := false
	defer func() {
		f.Close()
		if !ok {
			_ = os.Remove(tmp)
		}
	}()
	if err := f.Chmod(mode); err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		return err
	}
	if err := f.Sync(); err != nil {
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		return err
	}
	if err := syncDir(dir); err != nil {
		return err
	}
	ok = true
	return nil
}
