// SPDX-License-Identifier: AGPL-3.0-only

// package architecture contains lightweight dependency-direction checks.
package architecture

import (
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
)

const module = "github.com/GolemMC/Golem/"

func TestForbiddenImports(t *testing.T) {
	root := repositoryRoot(t)
	forbidden := map[string][]string{
		"internal/protocol":    {"internal/game", "internal/world", "internal/session", "internal/server"},
		"internal/world":       {"internal/game", "internal/protocol", "internal/session", "internal/server"},
		"internal/registry":    {"internal/game", "internal/world", "internal/session", "internal/server"},
		"internal/diagnostics": {"internal/game", "internal/session", "internal/server"},
		"internal/game":        {"internal/protocol", "internal/session", "internal/server", "internal/world/region"},
		"internal/session":     {"internal/server", "internal/world/region"},
	}
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() || !strings.HasSuffix(path, ".go") {
			return nil
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		relative = filepath.ToSlash(relative)
		file, err := parser.ParseFile(token.NewFileSet(), path, nil, parser.ImportsOnly)
		if err != nil {
			return err
		}
		for _, imported := range file.Imports {
			pathValue, _ := strconv.Unquote(imported.Path.Value)
			if strings.HasPrefix(pathValue, "github.com/Tnze/go-mc") && !allowedGoMC(relative) {
				t.Errorf("%s directly imports go-mc; use a Golem-owned adapter", relative)
			}
			for owner, denied := range forbidden {
				if relative != owner && !strings.HasPrefix(relative, owner+"/") {
					continue
				}
				for _, dependency := range denied {
					if pathValue == module+dependency || strings.HasPrefix(pathValue, module+dependency+"/") {
						t.Errorf("%s imports forbidden dependency %s", relative, pathValue)
					}
				}
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestNoVaguePackageNames(t *testing.T) {
	root := repositoryRoot(t)
	for _, name := range []string{"utils", "helpers", "common", "manager", "misc", "shared"} {
		if _, err := os.Stat(filepath.Join(root, "internal", name)); !os.IsNotExist(err) {
			t.Errorf("vague package internal/%s is not allowed", name)
		}
	}
}

func allowedGoMC(relative string) bool {
	for _, prefix := range []string{"internal/protocol/", "internal/world/", "internal/registry/", "tools/registrygen/"} {
		if strings.HasPrefix(relative, prefix) {
			return true
		}
	}
	return false
}

func repositoryRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("cannot locate architecture test")
	}
	return filepath.Clean(filepath.Join(filepath.Dir(file), "..", ".."))
}
