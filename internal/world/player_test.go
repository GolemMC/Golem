// SPDX-License-Identifier: AGPL-3.0-only

package world

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestPlayerDataRoundTripPreservesUnknownFields(t *testing.T) {
	w := &World{Path: t.TempDir()}
	id := [16]byte{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16}
	want := PlayerData{
		Position: [3]float64{12.5, 64, -8.25}, Rotation: [2]float32{90, -10}, SelectedHotbar: 4,
		Inventory: []InventoryItem{{Slot: 0, ID: "minecraft:stone", Count: 64, Raw: map[string]any{"components": map[string]any{"minecraft:custom_data": map[string]any{"kept": int8(1)}}}}},
		Raw:       map[string]any{"CustomField": "preserve-me"},
	}
	if err := w.SavePlayer(context.Background(), id, want); err != nil {
		t.Fatal(err)
	}
	got, err := w.LoadPlayer(id)
	if err != nil {
		t.Fatal(err)
	}
	if !got.Exists || got.Position != want.Position || got.Rotation != want.Rotation || got.SelectedHotbar != want.SelectedHotbar {
		t.Fatalf("round trip got=%#v want=%#v", got, want)
	}
	if got.Raw["CustomField"] != "preserve-me" || got.Raw["playerGameType"] != int32(1) || got.Raw["Dimension"] != "minecraft:overworld" {
		t.Fatalf("required or unknown fields missing: %#v", got.Raw)
	}
	if len(got.Inventory) != 1 || got.Inventory[0].ID != "minecraft:stone" || got.Inventory[0].Raw["components"] == nil {
		t.Fatalf("inventory was not preserved: %#v", got.Inventory)
	}
}

func TestSavePlayerDoesNotMutateCallerData(t *testing.T) {
	w := &World{Path: t.TempDir()}
	player := PlayerData{
		Position:  [3]float64{1, 64, 2},
		Raw:       map[string]any{"CustomField": "kept"},
		Inventory: []InventoryItem{{Slot: 0, ID: "minecraft:stone", Count: 1, Raw: map[string]any{"CustomItemField": "kept"}}},
	}
	if err := w.SavePlayer(context.Background(), [16]byte{1}, player); err != nil {
		t.Fatal(err)
	}
	if _, exists := player.Raw["Pos"]; exists {
		t.Fatal("SavePlayer mutated the caller's raw player compound")
	}
	if _, exists := player.Inventory[0].Raw["count"]; exists {
		t.Fatal("SavePlayer mutated the caller's raw inventory compound")
	}
}

func TestMalformedPlayerDataIsNotOverwrittenByLoad(t *testing.T) {
	w := &World{Path: t.TempDir()}
	id := [16]byte{1}
	path := filepath.Join(w.Path, "playerdata", formatUUID(id)+".dat")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	want := []byte("not gzip")
	if err := os.WriteFile(path, want, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := w.LoadPlayer(id); err == nil {
		t.Fatal("malformed player data was accepted")
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(want) {
		t.Fatal("malformed player file changed")
	}
}

func TestPlayerFilenameUsesDashedUUID(t *testing.T) {
	id := [16]byte{0x12, 0x34, 0x56, 0x78, 0x12, 0x34, 0x56, 0x78, 0x90, 0xab, 0xcd, 0xef, 0x12, 0x34, 0x56, 0x78}
	if got := formatUUID(id); got != "12345678-1234-5678-90ab-cdef12345678" {
		t.Fatalf("UUID filename=%q", got)
	}
}
