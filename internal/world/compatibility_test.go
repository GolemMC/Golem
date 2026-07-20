// SPDX-License-Identifier: AGPL-3.0-only

package world

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"testing"

	"github.com/GolemMC/Golem/internal/world/region"
)

// TestExternalVanillaWorldRead is opt-in so a disposable vanilla world can be
// checked without making a repository-specific save part of the test suite.
func TestExternalVanillaWorldRead(t *testing.T) {
	path := os.Getenv("GOLEM_TEST_WORLD")
	if path == "" {
		t.Skip("set GOLEM_TEST_WORLD to a disposable vanilla 1.21.1 world")
	}
	files, err := filepath.Glob(filepath.Join(path, "region", "r.*.*.mca"))
	if err != nil || len(files) == 0 {
		t.Fatalf("find region files: count=%d err=%v", len(files), err)
	}
	w := &World{Path: path, loaded: make(map[[2]int32]struct{})}
	chunks := 0
	entities := 0
	entityTypes := make(map[string]int)
	for _, file := range files {
		var regionX, regionZ int32
		if _, err := fmt.Sscanf(filepath.Base(file), "r.%d.%d.mca", &regionX, &regionZ); err != nil {
			t.Fatalf("parse region name %q: %v", file, err)
		}
		f, err := os.Open(file)
		if err != nil {
			t.Fatal(err)
		}
		st, err := f.Stat()
		if err != nil {
			f.Close()
			t.Fatal(err)
		}
		headerBytes := make([]byte, region.HeaderBytes)
		_, readErr := io.ReadFull(f, headerBytes)
		f.Close()
		if readErr != nil {
			t.Fatal(readErr)
		}
		header, err := region.ParseHeader(headerBytes, st.Size())
		if err != nil {
			t.Fatal(err)
		}
		for index, location := range header {
			if location.SectorOffset == 0 {
				continue
			}
			localX, localZ := int32(index%32), int32(index/32)
			if _, err := w.LoadChunk(regionX*32+localX, regionZ*32+localZ); err != nil {
				t.Fatalf("load %s chunk %d: %v", filepath.Base(file), index, err)
			}
			loadedEntities, err := w.LoadEntities(regionX*32+localX, regionZ*32+localZ)
			if err != nil {
				t.Fatalf("load %s entity chunk %d: %v", filepath.Base(file), index, err)
			}
			entities += len(loadedEntities)
			for _, entity := range loadedEntities {
				entityTypes[entity.Type]++
			}
			chunks++
		}
	}
	if chunks == 0 {
		t.Fatal("world contains no existing chunks")
	}
	t.Logf("decoded %d chunks and %d entities from %d region files", chunks, entities, len(files))
	types := make([]string, 0, len(entityTypes))
	for entityType, count := range entityTypes {
		types = append(types, fmt.Sprintf("%s=%d", entityType, count))
	}
	sort.Strings(types)
	t.Logf("entity types: %v", types)
}
