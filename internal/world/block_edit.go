// SPDX-License-Identifier: AGPL-3.0-only

package world

import (
	"bytes"
	"fmt"
	"math/bits"

	"github.com/GolemMC/Golem/internal/version"
	"github.com/GolemMC/Golem/internal/world/region"
	"github.com/Tnze/go-mc/nbt"
)

const maxAtomicBlockEdits = 64

// BlockEdit describes one block mutation in an existing chunk. SetBlocks
// commits a bounded group atomically through one Anvil chunk rewrite.
type BlockEdit struct {
	X, Y, Z int32
	State   BlockState
}

func (w *World) GetBlock(x, y, z int32) (BlockState, error) {
	if y < -64 || y > 319 {
		return BlockState{}, fmt.Errorf("block Y %d outside Overworld build height", y)
	}
	chunk, err := w.LoadChunk(x>>4, z>>4)
	if err != nil {
		return BlockState{}, err
	}
	section, ok := chunk.Sections[int8(y>>4)]
	if !ok || len(section.BlockStates.Palette) == 0 {
		return BlockState{Name: "minecraft:air"}, nil
	}
	indices, err := unpackDiskPalette(section.BlockStates.Data, len(section.BlockStates.Palette), 4096, 4)
	if err != nil {
		return BlockState{}, err
	}
	index := int((y&15)<<8 | (z&15)<<4 | (x & 15))
	return section.BlockStates.Palette[indices[index]], nil
}

func (w *World) SetBlock(x, y, z int32, state BlockState) (BlockState, error) {
	old, err := w.SetBlocks([]BlockEdit{{X: x, Y: y, Z: z, State: state}})
	if err != nil {
		return BlockState{}, err
	}
	return old[0], nil
}

// SetBlocks changes multiple blocks only when every edit belongs to the same
// existing chunk and every edit validates. The live region is replaced once,
// so a crash cannot publish only one half of a door or tall plant.
func (w *World) SetBlocks(edits []BlockEdit) ([]BlockState, error) {
	if len(edits) == 0 || len(edits) > maxAtomicBlockEdits {
		return nil, fmt.Errorf("atomic block edit count %d outside 1..%d", len(edits), maxAtomicBlockEdits)
	}
	chunkX, chunkZ := edits[0].X>>4, edits[0].Z>>4
	seen := make(map[[3]int32]struct{}, len(edits))
	for i, edit := range edits {
		if edit.Y < -64 || edit.Y > 319 {
			return nil, fmt.Errorf("block edit %d Y %d outside Overworld build height", i, edit.Y)
		}
		if edit.X>>4 != chunkX || edit.Z>>4 != chunkZ {
			return nil, fmt.Errorf("atomic block edits span multiple chunks")
		}
		position := [3]int32{edit.X, edit.Y, edit.Z}
		if _, duplicate := seen[position]; duplicate {
			return nil, fmt.Errorf("duplicate atomic block edit at (%d,%d,%d)", edit.X, edit.Y, edit.Z)
		}
		seen[position] = struct{}{}
	}
	regionX, localChunkX := region.WorldToRegion(chunkX)
	regionZ, localChunkZ := region.WorldToRegion(chunkZ)
	path := region.RegionPath(w.Path, regionX, regionZ)
	w.editMu.Lock()
	defer w.editMu.Unlock()
	rawNBT, err := region.ReadChunk(path, int(localChunkX), int(localChunkZ))
	if err != nil {
		return nil, err
	}
	var root map[string]any
	if _, err := nbt.NewDecoder(bytes.NewReader(rawNBT)).Decode(&root); err != nil {
		return nil, fmt.Errorf("decode editable chunk: %w", err)
	}
	if root["DataVersion"] != int32(version.WorldDataVersion) || root["xPos"] != chunkX || root["zPos"] != chunkZ {
		return nil, fmt.Errorf("editable chunk metadata does not match 1.21.1 coordinates")
	}
	old := make([]BlockState, len(edits))
	changed := false
	for i, edit := range edits {
		previous, editChanged, err := setBlockInNBT(root, edit.X&15, edit.Y, edit.Z&15, edit.State)
		if err != nil {
			return nil, fmt.Errorf("block edit %d at (%d,%d,%d): %w", i, edit.X, edit.Y, edit.Z, err)
		}
		old[i] = previous
		changed = changed || editChanged
	}
	if !changed {
		return old, nil
	}
	encoded, err := nbt.Marshal(root)
	if err != nil {
		return nil, fmt.Errorf("encode edited chunk: %w", err)
	}
	if w.regions == nil {
		w.regions = region.NewStore()
	}
	if err := w.regions.WriteChunk(path, int(localChunkX), int(localChunkZ), encoded); err != nil {
		return nil, err
	}
	return old, nil
}

func setBlockInNBT(root map[string]any, localX, y, localZ int32, state BlockState) (BlockState, bool, error) {
	hasBlockEntity, err := blockEntityAt(root, localX, y, localZ)
	if err != nil {
		return BlockState{}, false, err
	}
	if hasBlockEntity {
		return BlockState{}, false, fmt.Errorf("refusing to replace block entity at local (%d,%d,%d)", localX, y, localZ)
	}
	sections, ok := root["sections"].([]any)
	if !ok {
		return BlockState{}, false, fmt.Errorf("chunk sections are missing or malformed")
	}
	sectionY := int8(y >> 4)
	var section map[string]any
	for _, rawSection := range sections {
		candidate, ok := rawSection.(map[string]any)
		if !ok {
			return BlockState{}, false, fmt.Errorf("chunk section is not a compound")
		}
		if candidate["Y"] == sectionY {
			section = candidate
			break
		}
	}
	if section == nil {
		return BlockState{}, false, fmt.Errorf("existing chunk has no section Y=%d", sectionY)
	}
	blockStates, ok := section["block_states"].(map[string]any)
	if !ok {
		return BlockState{}, false, fmt.Errorf("section Y=%d has no block_states", sectionY)
	}
	rawPalette, ok := blockStates["palette"].([]any)
	if !ok || len(rawPalette) < 1 || len(rawPalette) > 4096 {
		return BlockState{}, false, fmt.Errorf("section Y=%d has invalid palette", sectionY)
	}
	palette := make([]BlockState, len(rawPalette))
	for i, rawEntry := range rawPalette {
		entry, ok := rawEntry.(map[string]any)
		if !ok {
			return BlockState{}, false, fmt.Errorf("palette entry %d is malformed", i)
		}
		name, ok := entry["Name"].(string)
		if !ok {
			return BlockState{}, false, fmt.Errorf("palette entry %d has no name", i)
		}
		properties := make(map[string]string)
		if rawProperties, exists := entry["Properties"]; exists {
			compound, ok := rawProperties.(map[string]any)
			if !ok {
				return BlockState{}, false, fmt.Errorf("palette entry %d properties are malformed", i)
			}
			for key, rawValue := range compound {
				value, ok := rawValue.(string)
				if !ok {
					return BlockState{}, false, fmt.Errorf("palette entry %d property %s is not a string", i, key)
				}
				properties[key] = value
			}
		}
		palette[i] = BlockState{Name: name, Properties: properties}
	}
	data, _ := blockStates["data"].([]int64)
	indices, err := unpackDiskPalette(data, len(palette), 4096, 4)
	if err != nil {
		return BlockState{}, false, err
	}
	index := int((y&15)<<8 | (localZ&15)<<4 | (localX & 15))
	old := palette[indices[index]]
	newPaletteIndex := -1
	for i, candidate := range palette {
		if equalBlockState(candidate, state) {
			newPaletteIndex = i
			break
		}
	}
	if newPaletteIndex < 0 {
		newPaletteIndex = len(palette)
		palette = append(palette, state)
		entry := map[string]any{"Name": state.Name}
		if len(state.Properties) != 0 {
			properties := make(map[string]any, len(state.Properties))
			for key, value := range state.Properties {
				properties[key] = value
			}
			entry["Properties"] = properties
		}
		rawPalette = append(rawPalette, entry)
		blockStates["palette"] = rawPalette
	}
	if indices[index] == newPaletteIndex {
		return old, false, nil
	}
	indices[index] = newPaletteIndex
	if len(palette) == 1 {
		delete(blockStates, "data")
	} else {
		blockStates["data"] = packDiskPalette(indices, max(4, bits.Len(uint(len(palette)-1))))
	}
	return old, true, nil
}

func blockEntityAt(root map[string]any, localX, y, localZ int32) (bool, error) {
	rawEntities, exists := root["block_entities"]
	if !exists {
		return false, nil
	}
	entities, ok := rawEntities.([]any)
	if !ok {
		return false, fmt.Errorf("chunk block_entities are malformed")
	}
	chunkX, _ := root["xPos"].(int32)
	chunkZ, _ := root["zPos"].(int32)
	wantX, wantZ := chunkX*16+(localX&15), chunkZ*16+(localZ&15)
	for _, raw := range entities {
		entity, ok := raw.(map[string]any)
		if !ok {
			return false, fmt.Errorf("chunk block entity is not a compound")
		}
		x, xOK := entity["x"].(int32)
		entityY, yOK := entity["y"].(int32)
		z, zOK := entity["z"].(int32)
		if !xOK || !yOK || !zOK {
			return false, fmt.Errorf("chunk block entity has invalid coordinates")
		}
		if x == wantX && entityY == y && z == wantZ {
			return true, nil
		}
	}
	return false, nil
}

func unpackDiskPalette(data []int64, paletteSize, values, minimumBits int) ([]int, error) {
	indices := make([]int, values)
	if paletteSize == 1 {
		if len(data) != 0 {
			return nil, fmt.Errorf("single-valued disk palette has data")
		}
		return indices, nil
	}
	width := max(minimumBits, bits.Len(uint(paletteSize-1)))
	perLong := 64 / width
	expected := (values + perLong - 1) / perLong
	if len(data) != expected {
		return nil, fmt.Errorf("disk palette has %d longs; expected %d", len(data), expected)
	}
	mask := uint64(1<<width) - 1
	for i := range indices {
		index := int(uint64(data[i/perLong]) >> ((i % perLong) * width) & mask)
		if index >= paletteSize {
			return nil, fmt.Errorf("disk palette index %d outside %d entries", index, paletteSize)
		}
		indices[i] = index
	}
	return indices, nil
}

func packDiskPalette(values []int, width int) []int64 {
	perLong := 64 / width
	longs := make([]int64, (len(values)+perLong-1)/perLong)
	for i, value := range values {
		longs[i/perLong] |= int64(uint64(value) << ((i % perLong) * width))
	}
	return longs
}

func equalBlockState(a, b BlockState) bool {
	if a.Name != b.Name || len(a.Properties) != len(b.Properties) {
		return false
	}
	for key, value := range a.Properties {
		if b.Properties[key] != value {
			return false
		}
	}
	return true
}
