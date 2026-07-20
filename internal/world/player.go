// SPDX-License-Identifier: AGPL-3.0-only

package world

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/Tnze/go-mc/nbt"
)

const maxPlayerDataBytes = 16 << 20

var ErrPlayerDataMalformed = errors.New("player data is malformed")

type PlayerData struct {
	Position       [3]float64
	Rotation       [2]float32
	SelectedHotbar int32
	Inventory      []InventoryItem
	Raw            map[string]any
	Exists         bool
}

type InventoryItem struct {
	Slot  int8
	ID    string
	Count int32
	Raw   map[string]any
}

func (w *World) LoadPlayer(id [16]byte) (PlayerData, error) {
	path := filepath.Join(w.Path, "playerdata", formatUUID(id)+".dat")
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return PlayerData{Raw: make(map[string]any)}, nil
		}
		return PlayerData{}, err
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return PlayerData{}, fmt.Errorf("%w: open %q: %v", ErrPlayerDataMalformed, path, err)
	}
	defer gz.Close()
	data, err := io.ReadAll(io.LimitReader(gz, maxPlayerDataBytes+1))
	if err != nil {
		return PlayerData{}, fmt.Errorf("%w: decompress %q: %v", ErrPlayerDataMalformed, path, err)
	}
	if len(data) > maxPlayerDataBytes {
		return PlayerData{}, fmt.Errorf("%w: %q exceeds %d bytes", ErrPlayerDataMalformed, path, maxPlayerDataBytes)
	}
	var raw map[string]any
	if _, err := nbt.NewDecoder(bytes.NewReader(data)).Decode(&raw); err != nil {
		return PlayerData{}, fmt.Errorf("%w: decode %q: %v", ErrPlayerDataMalformed, path, err)
	}
	position, err := numericList[float64](raw["Pos"], 3)
	if err != nil {
		return PlayerData{}, fmt.Errorf("%w: Pos in %q: %v", ErrPlayerDataMalformed, path, err)
	}
	rotation, err := numericList[float32](raw["Rotation"], 2)
	if err != nil {
		return PlayerData{}, fmt.Errorf("%w: Rotation in %q: %v", ErrPlayerDataMalformed, path, err)
	}
	selected := int32(0)
	if value, exists := raw["SelectedItemSlot"]; exists {
		parsed, ok := value.(int32)
		if !ok || parsed < 0 || parsed > 8 {
			return PlayerData{}, fmt.Errorf("%w: invalid SelectedItemSlot in %q", ErrPlayerDataMalformed, path)
		}
		selected = parsed
	}
	inventory, err := decodeInventory(raw["Inventory"])
	if err != nil {
		return PlayerData{}, fmt.Errorf("%w: Inventory in %q: %v", ErrPlayerDataMalformed, path, err)
	}
	return PlayerData{Position: [3]float64{position[0], position[1], position[2]}, Rotation: [2]float32{rotation[0], rotation[1]}, SelectedHotbar: selected, Inventory: inventory, Raw: raw, Exists: true}, nil
}

func (w *World) SavePlayer(ctx context.Context, id [16]byte, player PlayerData) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	raw := player.Raw
	if raw == nil {
		raw = make(map[string]any)
	}
	raw["Pos"] = []float64{player.Position[0], player.Position[1], player.Position[2]}
	raw["Rotation"] = []float32{player.Rotation[0], player.Rotation[1]}
	raw["playerGameType"] = int32(1)
	raw["SelectedItemSlot"] = player.SelectedHotbar
	raw["Dimension"] = "minecraft:overworld"
	inventory := make([]any, 0, len(player.Inventory))
	for _, item := range player.Inventory {
		entry := item.Raw
		if entry == nil {
			entry = make(map[string]any)
		}
		entry["Slot"] = item.Slot
		entry["id"] = item.ID
		entry["count"] = item.Count
		inventory = append(inventory, entry)
	}
	raw["Inventory"] = inventory
	encoded, err := nbt.Marshal(raw)
	if err != nil {
		return fmt.Errorf("encode player data: %w", err)
	}
	var compressed bytes.Buffer
	gz := gzip.NewWriter(&compressed)
	if _, err := gz.Write(encoded); err != nil {
		return err
	}
	if err := gz.Close(); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	path := filepath.Join(w.Path, "playerdata", formatUUID(id)+".dat")
	if err := AtomicWriteFile(path, compressed.Bytes(), 0o600); err != nil {
		return fmt.Errorf("save player data %q: %w", path, err)
	}
	return nil
}

func decodeInventory(value any) ([]InventoryItem, error) {
	if value == nil {
		return nil, nil
	}
	list, ok := value.([]any)
	if !ok {
		return nil, fmt.Errorf("expected compound list")
	}
	items := make([]InventoryItem, 0, len(list))
	seen := make(map[int8]struct{}, len(list))
	for index, rawEntry := range list {
		entry, ok := rawEntry.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("entry %d is %T", index, rawEntry)
		}
		slot, slotOK := entry["Slot"].(int8)
		id, idOK := entry["id"].(string)
		count, countOK := entry["count"].(int32)
		if !slotOK || !idOK || !countOK || !validInventorySlot(slot) || count < 1 || count > 99 {
			return nil, fmt.Errorf("entry %d has invalid slot, id, or count", index)
		}
		if _, duplicate := seen[slot]; duplicate {
			return nil, fmt.Errorf("duplicate slot %d", slot)
		}
		seen[slot] = struct{}{}
		items = append(items, InventoryItem{Slot: slot, ID: id, Count: count, Raw: entry})
	}
	return items, nil
}

func validInventorySlot(slot int8) bool {
	return slot >= 0 && slot <= 35 || slot >= 100 && slot <= 103 || slot == -106
}

func numericList[T ~float32 | ~float64](value any, expected int) ([]T, error) {
	list, ok := value.([]any)
	if !ok || len(list) != expected {
		return nil, fmt.Errorf("expected list of %d numbers", expected)
	}
	out := make([]T, expected)
	for i, item := range list {
		number, ok := item.(T)
		if !ok {
			return nil, fmt.Errorf("element %d has type %T", i, item)
		}
		out[i] = number
	}
	return out, nil
}

func formatUUID(id [16]byte) string {
	hexID := hex.EncodeToString(id[:])
	return hexID[:8] + "-" + hexID[8:12] + "-" + hexID[12:16] + "-" + hexID[16:20] + "-" + hexID[20:]
}
