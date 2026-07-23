// SPDX-License-Identifier: AGPL-3.0-only

package world

import (
	"encoding/binary"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

type Lock struct {
	file *os.File
	path string
}

func AcquireLock(worldPath string) (*Lock, error) {
	path := filepath.Join(worldPath, "session.lock")
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open world lock %q: %w", path, err)
	}
	if err := lockFile(f); err != nil {
		f.Close()
		return nil, fmt.Errorf("world %q is already in use (cannot acquire session.lock): %w", worldPath, err)
	}
	var stamp [8]byte
	binary.BigEndian.PutUint64(stamp[:], uint64(time.Now().UnixMilli()))
	if err := f.Truncate(0); err != nil {
		f.Close()
		return nil, fmt.Errorf("truncate world lock: %w", err)
	}
	if _, err := f.WriteAt(stamp[:], 0); err != nil {
		f.Close()
		return nil, fmt.Errorf("write world lock: %w", err)
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return nil, fmt.Errorf("sync world lock: %w", err)
	}
	return &Lock{file: f, path: path}, nil
}

func (l *Lock) Held() bool { return l != nil && l.file != nil }

func (l *Lock) Close() error {
	if l == nil || l.file == nil {
		return nil
	}
	err1 := unlockFile(l.file)
	err2 := l.file.Close()
	l.file = nil
	if err1 != nil {
		return fmt.Errorf("unlock %q: %w", l.path, err1)
	}
	if err2 != nil {
		return fmt.Errorf("close lock %q: %w", l.path, err2)
	}
	return nil
}
