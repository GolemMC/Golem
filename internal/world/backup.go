// SPDX-License-Identifier: AGPL-3.0-only

package world

import (
	"archive/zip"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

func CreateBackup(worldPath, backupDir string, retention int, now time.Time) (string, error) {
	if retention < 1 {
		return "", fmt.Errorf("backup retention must be positive")
	}
	if err := os.MkdirAll(backupDir, 0o750); err != nil {
		return "", fmt.Errorf("create backup directory: %w", err)
	}
	base := filepath.Base(filepath.Clean(worldPath))
	name := fmt.Sprintf("%s-%s.zip", base, now.UTC().Format("20060102T150405.000000000Z"))
	final := filepath.Join(backupDir, name)
	tmp, err := os.CreateTemp(backupDir, ".golem-backup-*.tmp")
	if err != nil {
		return "", fmt.Errorf("create backup temporary file: %w", err)
	}
	tmpName := tmp.Name()
	ok := false
	defer func() {
		tmp.Close()
		if !ok {
			_ = os.Remove(tmpName)
		}
	}()
	zw := zip.NewWriter(tmp)
	err = filepath.WalkDir(worldPath, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if path == filepath.Join(worldPath, "session.lock") {
			return nil
		}
		rel, err := filepath.Rel(worldPath, path)
		if err != nil {
			return err
		}
		if rel == "." {
			return nil
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("refusing to back up symlink %q", path)
		}
		if entry.IsDir() {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		h, err := zip.FileInfoHeader(info)
		if err != nil {
			return err
		}
		h.Name = filepath.ToSlash(rel)
		h.Method = zip.Deflate
		w, err := zw.CreateHeader(h)
		if err != nil {
			return err
		}
		f, err := os.Open(path)
		if err != nil {
			return err
		}
		_, copyErr := io.Copy(w, f)
		closeErr := f.Close()
		if copyErr != nil {
			return copyErr
		}
		return closeErr
	})
	if err != nil {
		_ = zw.Close()
		return "", fmt.Errorf("archive world: %w", err)
	}
	if err := zw.Close(); err != nil {
		return "", fmt.Errorf("finalize backup archive: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		return "", fmt.Errorf("sync backup archive: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return "", fmt.Errorf("close backup archive: %w", err)
	}
	if err := os.Rename(tmpName, final); err != nil {
		return "", fmt.Errorf("publish backup archive: %w", err)
	}
	if err := syncDir(backupDir); err != nil {
		return "", err
	}
	ok = true
	if err := pruneBackups(backupDir, base+"-", retention); err != nil {
		return final, err
	}
	return final, nil
}

func pruneBackups(dir, prefix string, retention int) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	var names []string
	for _, e := range entries {
		if !e.IsDir() && strings.HasPrefix(e.Name(), prefix) && strings.HasSuffix(e.Name(), ".zip") {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)
	for len(names) > retention {
		if err := os.Remove(filepath.Join(dir, names[0])); err != nil {
			return fmt.Errorf("remove expired backup %q: %w", names[0], err)
		}
		names = names[1:]
	}
	return nil
}

func syncDir(path string) error {
	d, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("open directory for sync %q: %w", path, err)
	}
	defer d.Close()
	if err := d.Sync(); err != nil {
		return fmt.Errorf("sync directory %q: %w", path, err)
	}
	return nil
}
