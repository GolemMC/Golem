// SPDX-License-Identifier: AGPL-3.0-only

package world

import (
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"

	"github.com/GolemMC/Golem/internal/version"
	"github.com/GolemMC/Golem/internal/world/region"
	"github.com/Tnze/go-mc/nbt"
)

func TestSetBlockPersistsPaletteEdit(t *testing.T) {
	dir := t.TempDir()
	root := map[string]any{
		"DataVersion": int32(version.WorldDataVersion),
		"xPos":        int32(0),
		"zPos":        int32(0),
		"Status":      "minecraft:full",
		"sections": []any{map[string]any{
			"Y": int8(0),
			"block_states": map[string]any{
				"palette": []any{map[string]any{"Name": "minecraft:stone"}},
			},
			"biomes": map[string]any{"palette": []any{"minecraft:plains"}},
		}},
		"Heightmaps": map[string]any{},
	}
	encoded, err := nbt.Marshal(root)
	if err != nil {
		t.Fatal(err)
	}
	writeTestRegion(t, dir, encoded)
	w := &World{Path: dir, regions: region.NewStore()}
	old, err := w.SetBlock(1, 2, 3, BlockState{Name: "minecraft:air"})
	if err != nil {
		t.Fatal(err)
	}
	if old.Name != "minecraft:stone" {
		t.Fatalf("old block = %q", old.Name)
	}
	chunk, err := w.LoadChunk(0, 0)
	if err != nil {
		t.Fatal(err)
	}
	section := chunk.Sections[0]
	indices, err := unpackDiskPalette(section.BlockStates.Data, len(section.BlockStates.Palette), 4096, 4)
	if err != nil {
		t.Fatal(err)
	}
	index := (2 << 8) | (3 << 4) | 1
	if got := section.BlockStates.Palette[indices[index]].Name; got != "minecraft:air" {
		t.Fatalf("persisted block = %q", got)
	}
	if rootStatus := readRawChunk(t, dir)["Status"]; rootStatus != "minecraft:full" {
		t.Fatalf("unknown root field was not preserved: %v", rootStatus)
	}
}

func TestSetBlocksCommitsBothStatesInOneChunkRewrite(t *testing.T) {
	dir := t.TempDir()
	root := map[string]any{
		"DataVersion": int32(version.WorldDataVersion), "xPos": int32(0), "zPos": int32(0),
		"sections": []any{map[string]any{
			"Y":            int8(4),
			"block_states": map[string]any{"palette": []any{map[string]any{"Name": "minecraft:air"}}},
			"biomes":       map[string]any{"palette": []any{"minecraft:plains"}},
		}},
	}
	encoded, err := nbt.Marshal(root)
	if err != nil {
		t.Fatal(err)
	}
	writeTestRegion(t, dir, encoded)
	w := &World{Path: dir, regions: region.NewStore()}
	lower := BlockState{Name: "minecraft:oak_door", Properties: map[string]string{"facing": "north", "half": "lower", "hinge": "left", "open": "false", "powered": "false"}}
	upper := BlockState{Name: "minecraft:oak_door", Properties: map[string]string{"facing": "north", "half": "upper", "hinge": "left", "open": "false", "powered": "false"}}
	old, err := w.SetBlocks([]BlockEdit{{X: 1, Y: 64, Z: 1, State: lower}, {X: 1, Y: 65, Z: 1, State: upper}})
	if err != nil {
		t.Fatal(err)
	}
	if len(old) != 2 || !isAirBlock(old[0]) || !isAirBlock(old[1]) {
		t.Fatalf("old states=%+v", old)
	}
	for y, want := range map[int32]BlockState{64: lower, 65: upper} {
		got, err := w.GetBlock(1, y, 1)
		if err != nil || !equalBlockState(got, want) {
			t.Fatalf("block y=%d got=%+v want=%+v err=%v", y, got, want, err)
		}
	}
}

func TestSetBlocksValidationLeavesRegionUnchanged(t *testing.T) {
	dir := t.TempDir()
	root := map[string]any{
		"DataVersion": int32(version.WorldDataVersion), "xPos": int32(0), "zPos": int32(0),
		"sections": []any{map[string]any{
			"Y":            int8(0),
			"block_states": map[string]any{"palette": []any{map[string]any{"Name": "minecraft:stone"}}},
			"biomes":       map[string]any{"palette": []any{"minecraft:plains"}},
		}},
	}
	encoded, err := nbt.Marshal(root)
	if err != nil {
		t.Fatal(err)
	}
	writeTestRegion(t, dir, encoded)
	path := region.RegionPath(dir, 0, 0)
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	w := &World{Path: dir, regions: region.NewStore()}
	_, err = w.SetBlocks([]BlockEdit{
		{X: 1, Y: 1, Z: 1, State: BlockState{Name: "minecraft:air"}},
		{X: 1, Y: 16, Z: 1, State: BlockState{Name: "minecraft:air"}}, // missing section
	})
	if err == nil {
		t.Fatal("atomic edit with an invalid second block was accepted")
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Fatal("region changed after rejected atomic edit")
	}
}

func TestSetBlockRejectsMissingSectionWithoutChangingRegion(t *testing.T) {
	dir := t.TempDir()
	root := map[string]any{"DataVersion": int32(version.WorldDataVersion), "xPos": int32(0), "zPos": int32(0), "sections": []any{}}
	encoded, err := nbt.Marshal(root)
	if err != nil {
		t.Fatal(err)
	}
	writeTestRegion(t, dir, encoded)
	path := region.RegionPath(dir, 0, 0)
	before, _ := os.ReadFile(path)
	w := &World{Path: dir, regions: region.NewStore()}
	if _, err := w.SetBlock(0, 0, 0, BlockState{Name: "minecraft:air"}); err == nil {
		t.Fatal("expected missing section error")
	}
	after, _ := os.ReadFile(path)
	if string(before) != string(after) {
		t.Fatal("region changed after rejected edit")
	}
}

func TestSetBlockRefusesToOrphanBlockEntity(t *testing.T) {
	root := map[string]any{
		"xPos": int32(-1), "zPos": int32(2),
		"block_entities": []any{map[string]any{
			"id": "minecraft:chest", "x": int32(-15), "y": int32(64), "z": int32(35),
		}},
	}
	found, err := blockEntityAt(root, 1, 64, 3)
	if err != nil || !found {
		t.Fatal("block entity coordinate was not detected")
	}
}

func writeTestRegion(t *testing.T, dir string, data []byte) {
	t.Helper()
	record, err := region.EncodeZlibRecord(data)
	if err != nil {
		t.Fatal(err)
	}
	sectors := (len(record) + region.SectorBytes - 1) / region.SectorBytes
	file := make([]byte, region.HeaderBytes+sectors*region.SectorBytes)
	binary.BigEndian.PutUint32(file[:4], 2<<8|uint32(sectors))
	copy(file[region.HeaderBytes:], record)
	path := region.RegionPath(dir, 0, 0)
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, file, 0o600); err != nil {
		t.Fatal(err)
	}
}

func readRawChunk(t *testing.T, dir string) map[string]any {
	t.Helper()
	raw, err := region.ReadChunk(region.RegionPath(dir, 0, 0), 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	var root map[string]any
	if err := nbt.Unmarshal(raw, &root); err != nil {
		t.Fatal(err)
	}
	return root
}

func isAirBlock(state BlockState) bool {
	return state.Name == "minecraft:air" || state.Name == "minecraft:cave_air" || state.Name == "minecraft:void_air"
}
