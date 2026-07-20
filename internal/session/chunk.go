// SPDX-License-Identifier: AGPL-3.0-only

package session

import (
	"errors"
	"fmt"
	"math/bits"
	"strings"

	"github.com/GolemMC/Golem/internal/protocol"
	"github.com/GolemMC/Golem/internal/registry"
	"github.com/GolemMC/Golem/internal/world"
)

// encodeWorldChunk is the protocol-facing adapter between the world model and
// protocol 767. Storage code never imports packet identifiers.
func encodeWorldChunk(chunk world.Chunk) ([]byte, error) {
	var output protocol.Encoder
	output.Int32(chunk.X)
	output.Int32(chunk.Z)
	heightmaps, err := encodeHeightmaps(chunk.Heightmaps)
	if err != nil {
		return nil, err
	}
	output.Write(heightmaps)
	var sections protocol.Encoder
	for y := int8(-4); y <= 19; y++ {
		section, exists := chunk.Sections[y]
		if !exists || len(section.BlockStates.Palette) == 0 {
			section.BlockStates.Palette = []world.BlockState{{Name: "minecraft:air"}}
		}
		if !exists || len(section.Biomes.Palette) == 0 {
			section.Biomes.Palette = []string{"minecraft:plains"}
		}
		nonAir, blockData, err := encodeBlockStates(section.BlockStates)
		if err != nil {
			return nil, fmt.Errorf("section Y=%d blocks: %w", y, err)
		}
		biomeData, err := encodeBiomes(section.Biomes)
		if err != nil {
			return nil, fmt.Errorf("section Y=%d biomes: %w", y, err)
		}
		sections.Int16(int16(nonAir))
		sections.Write(blockData)
		sections.Write(biomeData)
	}
	output.ByteArray(sections.Bytes())
	output.VarInt(0)
	writeLightData(&output, chunk.Sections)
	return output.Bytes(), nil
}

func encodeBlockStates(container world.BlockPalette) (int, []byte, error) {
	global := make([]int32, len(container.Palette))
	air := make([]bool, len(container.Palette))
	for index, state := range container.Palette {
		id, err := registry.BlockStateID(state.Name, state.Properties)
		if err != nil {
			return 0, nil, err
		}
		global[index] = id
		name := strings.TrimPrefix(state.Name, "minecraft:")
		air[index] = name == "air" || name == "cave_air" || name == "void_air"
	}
	indices, err := unpackPalette(container.Data, len(global), 4096, 4)
	if err != nil {
		return 0, nil, err
	}
	nonAir := 0
	for _, index := range indices {
		if !air[index] {
			nonAir++
		}
	}
	var output protocol.Encoder
	if len(global) == 1 {
		output.WriteByte(0)
		output.VarInt(global[0])
		output.VarInt(0)
		return nonAir, output.Bytes(), nil
	}
	if len(global) <= 256 {
		width := uint8(max(4, bits.Len(uint(len(global)-1))))
		output.WriteByte(width)
		output.VarInt(int32(len(global)))
		for _, id := range global {
			output.VarInt(id)
		}
		writePacked(&output, indices, width)
		return nonAir, output.Bytes(), nil
	}
	width, err := registry.BlockStateBits()
	if err != nil {
		return 0, nil, err
	}
	direct := make([]int, len(indices))
	for index, paletteIndex := range indices {
		direct[index] = int(global[paletteIndex])
	}
	output.WriteByte(width)
	writePacked(&output, direct, width)
	return nonAir, output.Bytes(), nil
}

func encodeBiomes(container world.BiomePalette) ([]byte, error) {
	global := make([]int32, len(container.Palette))
	for index, name := range container.Palette {
		id, err := registry.EntryID("minecraft:worldgen/biome", name)
		if err != nil {
			return nil, err
		}
		global[index] = id
	}
	indices, err := unpackPalette(container.Data, len(global), 64, 1)
	if err != nil {
		return nil, err
	}
	var output protocol.Encoder
	if len(global) == 1 {
		output.WriteByte(0)
		output.VarInt(global[0])
		output.VarInt(0)
		return output.Bytes(), nil
	}
	if len(global) <= 8 {
		width := uint8(max(1, bits.Len(uint(len(global)-1))))
		output.WriteByte(width)
		output.VarInt(int32(len(global)))
		for _, id := range global {
			output.VarInt(id)
		}
		writePacked(&output, indices, width)
		return output.Bytes(), nil
	}
	count, err := registry.EntryCount("minecraft:worldgen/biome")
	if err != nil {
		return nil, err
	}
	width := uint8(bits.Len(uint(count - 1)))
	direct := make([]int, len(indices))
	for index, paletteIndex := range indices {
		direct[index] = int(global[paletteIndex])
	}
	output.WriteByte(width)
	writePacked(&output, direct, width)
	return output.Bytes(), nil
}

func unpackPalette(data []int64, paletteSize, values, minimumBits int) ([]int, error) {
	if paletteSize < 1 {
		return nil, errors.New("palette is empty")
	}
	indices := make([]int, values)
	if paletteSize == 1 {
		if len(data) != 0 {
			return nil, errors.New("single-valued palette has packed data")
		}
		return indices, nil
	}
	width := max(minimumBits, bits.Len(uint(paletteSize-1)))
	perLong := 64 / width
	expected := (values + perLong - 1) / perLong
	if len(data) != expected {
		return nil, fmt.Errorf("palette data has %d longs; expected %d", len(data), expected)
	}
	mask := uint64(1<<width) - 1
	for index := range indices {
		value := int((uint64(data[index/perLong]) >> ((index % perLong) * width)) & mask)
		if value >= paletteSize {
			return nil, fmt.Errorf("palette index %d outside %d entries", value, paletteSize)
		}
		indices[index] = value
	}
	return indices, nil
}

func writePacked(output *protocol.Encoder, values []int, width uint8) {
	perLong := 64 / int(width)
	longs := make([]uint64, (len(values)+perLong-1)/perLong)
	for index, value := range values {
		longs[index/perLong] |= uint64(value) << ((index % perLong) * int(width))
	}
	output.VarInt(int32(len(longs)))
	for _, value := range longs {
		output.Int64(int64(value))
	}
}

func encodeHeightmaps(saved map[string][]int64) ([]byte, error) {
	type networkHeightmaps struct {
		MotionBlocking []int64 `nbt:"MOTION_BLOCKING"`
		WorldSurface   []int64 `nbt:"WORLD_SURFACE"`
	}
	valuesByName := make(map[string][]int64, 2)
	for _, name := range []string{"MOTION_BLOCKING", "WORLD_SURFACE"} {
		values := saved[name]
		if len(values) == 0 {
			values = make([]int64, 37)
		}
		if len(values) != 37 {
			return nil, fmt.Errorf("heightmap %s has %d longs; expected 37", name, len(values))
		}
		valuesByName[name] = values
	}
	heightmaps := networkHeightmaps{MotionBlocking: valuesByName["MOTION_BLOCKING"], WorldSurface: valuesByName["WORLD_SURFACE"]}
	return protocol.EncodeNetworkNBT(heightmaps)
}

func writeLightData(output *protocol.Encoder, sections map[int8]world.ChunkSection) {
	var skyMask, blockMask, emptySkyMask, emptyBlockMask uint64
	var skyArrays, blockArrays [][]byte
	for bit := 0; bit < 26; bit++ {
		section, exists := sections[int8(bit-5)]
		if exists && len(section.SkyLight) == 2048 {
			skyMask |= 1 << bit
			skyArrays = append(skyArrays, section.SkyLight)
		} else {
			emptySkyMask |= 1 << bit
		}
		if exists && len(section.BlockLight) == 2048 {
			blockMask |= 1 << bit
			blockArrays = append(blockArrays, section.BlockLight)
		} else {
			emptyBlockMask |= 1 << bit
		}
	}
	for _, mask := range []uint64{skyMask, blockMask, emptySkyMask, emptyBlockMask} {
		output.VarInt(1)
		output.Int64(int64(mask))
	}
	for _, arrays := range [][][]byte{skyArrays, blockArrays} {
		output.VarInt(int32(len(arrays)))
		for _, light := range arrays {
			output.ByteArray(light)
		}
	}
}

func voidChunk(chunkX, chunkZ int32) []byte {
	chunk := world.Chunk{X: chunkX, Z: chunkZ, Sections: map[int8]world.ChunkSection{}, Heightmaps: map[string][]int64{}}
	packet, err := encodeWorldChunk(chunk)
	if err != nil {
		panic(err)
	}
	return packet
}
