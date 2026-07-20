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

func TestAppendDiskEntityIncludesPassengers(t *testing.T) {
	parentID := []int32{1, 2, 3, 4}
	passengerID := []int32{5, 6, 7, 8}
	entity := map[string]any{
		"id": "minecraft:boat", "UUID": parentID,
		"Pos":      []any{float64(1), float64(2), float64(3)},
		"Rotation": []any{float32(4), float32(5)},
		"Motion":   []any{float64(0), float64(0), float64(0)},
		"Passengers": []any{map[string]any{
			"id": "minecraft:pig", "UUID": passengerID,
			"Pos":      []any{float64(1), float64(2), float64(3)},
			"Rotation": []any{float32(0), float32(0)},
		}},
	}
	var got []Entity
	if err := appendDiskEntity(&got, make(map[[16]byte]struct{}), entity, 0); err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].Type != "minecraft:boat" || got[1].Type != "minecraft:pig" || got[1].Vehicle != got[0].UUID {
		t.Fatalf("entities=%+v", got)
	}
}

func TestLoadEntitiesFromEntityRegion(t *testing.T) {
	dir := t.TempDir()
	root := map[string]any{
		"DataVersion": int32(version.WorldDataVersion),
		"Position":    []int32{0, 0},
		"Entities": []any{map[string]any{
			"id": "minecraft:pig", "UUID": []int32{1, 2, 3, 4},
			"Pos":      []any{float64(1), float64(64), float64(2)},
			"Rotation": []any{float32(3), float32(4)},
			"Motion":   []any{float64(0), float64(0), float64(0)},
		}},
	}
	encoded, err := nbt.Marshal(root)
	if err != nil {
		t.Fatal(err)
	}
	record, err := region.EncodeZlibRecord(encoded)
	if err != nil {
		t.Fatal(err)
	}
	sectors := (len(record) + region.SectorBytes - 1) / region.SectorBytes
	file := make([]byte, region.HeaderBytes+sectors*region.SectorBytes)
	binary.BigEndian.PutUint32(file[:4], 2<<8|uint32(sectors))
	copy(file[region.HeaderBytes:], record)
	path := region.EntityRegionPath(dir, 0, 0)
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, file, 0o600); err != nil {
		t.Fatal(err)
	}
	entities, err := (&World{Path: dir}).LoadEntities(0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(entities) != 1 || entities[0].Type != "minecraft:pig" || entities[0].Position != ([3]float64{1, 64, 2}) {
		t.Fatalf("entities=%+v", entities)
	}
}

func TestAppendDiskEntityRejectsDuplicateUUID(t *testing.T) {
	entity := map[string]any{
		"id": "minecraft:pig", "UUID": []int32{1, 2, 3, 4},
		"Pos":      []any{float64(0), float64(0), float64(0)},
		"Rotation": []any{float32(0), float32(0)},
	}
	seen := make(map[[16]byte]struct{})
	var got []Entity
	if err := appendDiskEntity(&got, seen, entity, 0); err != nil {
		t.Fatal(err)
	}
	if err := appendDiskEntity(&got, seen, entity, 0); err == nil {
		t.Fatal("duplicate entity UUID was accepted")
	}
}
