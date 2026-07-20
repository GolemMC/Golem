// SPDX-License-Identifier: AGPL-3.0-only

package world

import (
	"bytes"
	"fmt"

	"github.com/GolemMC/Golem/internal/version"
	"github.com/GolemMC/Golem/internal/world/region"
	"github.com/Tnze/go-mc/nbt"
)

// ErrChunkMissing distinguishes absent terrain from corrupt or unreadable
// terrain without exposing the underlying region implementation.
var ErrChunkMissing = region.ErrChunkMissing

type BlockState struct {
	Name       string
	Properties map[string]string
}

type BlockPalette struct {
	Palette []BlockState
	Data    []int64
}

type BiomePalette struct {
	Palette []string
	Data    []int64
}

type ChunkSection struct {
	Y           int8
	BlockStates BlockPalette
	Biomes      BiomePalette
	SkyLight    []byte
	BlockLight  []byte
}

type Chunk struct {
	X, Z       int32
	Sections   map[int8]ChunkSection
	Heightmaps map[string][]int64
}

type diskChunk struct {
	DataVersion int32              `nbt:"DataVersion"`
	XPos        int32              `nbt:"xPos"`
	ZPos        int32              `nbt:"zPos"`
	Sections    []diskSection      `nbt:"sections"`
	Heightmaps  map[string][]int64 `nbt:"Heightmaps"`
}

type diskSection struct {
	Y           int8           `nbt:"Y"`
	BlockStates diskBlockState `nbt:"block_states"`
	Biomes      diskBiome      `nbt:"biomes"`
	SkyLight    []byte         `nbt:"SkyLight"`
	BlockLight  []byte         `nbt:"BlockLight"`
}

type diskBlockState struct {
	Palette []diskPaletteEntry `nbt:"palette"`
	Data    []int64            `nbt:"data"`
}

type diskPaletteEntry struct {
	Name       string            `nbt:"Name"`
	Properties map[string]string `nbt:"Properties"`
}

type diskBiome struct {
	Palette []string `nbt:"palette"`
	Data    []int64  `nbt:"data"`
}

func (w *World) LoadChunk(chunkX, chunkZ int32) (Chunk, error) {
	regionX, localX := region.WorldToRegion(chunkX)
	regionZ, localZ := region.WorldToRegion(chunkZ)
	path := region.RegionPath(w.Path, regionX, regionZ)
	raw, err := region.ReadChunk(path, int(localX), int(localZ))
	if err != nil {
		return Chunk{}, fmt.Errorf("read chunk (%d,%d): %w", chunkX, chunkZ, err)
	}
	chunk, err := DecodeChunk(raw, chunkX, chunkZ)
	if err != nil {
		return Chunk{}, fmt.Errorf("decode chunk (%d,%d): %w", chunkX, chunkZ, err)
	}
	w.mu.Lock()
	if w.loaded == nil {
		w.loaded = make(map[[2]int32]struct{})
	}
	key := [2]int32{chunkX, chunkZ}
	if _, exists := w.loaded[key]; !exists {
		w.loaded[key] = struct{}{}
		w.loadedChunks = len(w.loaded)
	}
	w.mu.Unlock()
	return chunk, nil
}

func DecodeChunk(data []byte, expectedX, expectedZ int32) (Chunk, error) {
	var disk diskChunk
	if _, err := nbt.NewDecoder(bytes.NewReader(data)).Decode(&disk); err != nil {
		return Chunk{}, err
	}
	if disk.DataVersion != version.WorldDataVersion {
		return Chunk{}, fmt.Errorf("data version %d is not supported; expected %d", disk.DataVersion, version.WorldDataVersion)
	}
	if disk.XPos != expectedX || disk.ZPos != expectedZ {
		return Chunk{}, fmt.Errorf("stored coordinates (%d,%d) do not match region coordinates (%d,%d)", disk.XPos, disk.ZPos, expectedX, expectedZ)
	}
	chunk := Chunk{X: disk.XPos, Z: disk.ZPos, Sections: make(map[int8]ChunkSection), Heightmaps: disk.Heightmaps}
	for _, section := range disk.Sections {
		if _, duplicate := chunk.Sections[section.Y]; duplicate {
			return Chunk{}, fmt.Errorf("duplicate section Y=%d", section.Y)
		}
		if len(section.SkyLight) != 0 && len(section.SkyLight) != 2048 {
			return Chunk{}, fmt.Errorf("section Y=%d has %d bytes of sky light", section.Y, len(section.SkyLight))
		}
		if len(section.BlockLight) != 0 && len(section.BlockLight) != 2048 {
			return Chunk{}, fmt.Errorf("section Y=%d has %d bytes of block light", section.Y, len(section.BlockLight))
		}
		if len(section.BlockStates.Palette) > 4096 {
			return Chunk{}, fmt.Errorf("section Y=%d block palette exceeds 4096 entries", section.Y)
		}
		if len(section.Biomes.Palette) > 64 {
			return Chunk{}, fmt.Errorf("section Y=%d biome palette exceeds 64 entries", section.Y)
		}
		blocks := make([]BlockState, len(section.BlockStates.Palette))
		for i, state := range section.BlockStates.Palette {
			blocks[i] = BlockState{Name: state.Name, Properties: state.Properties}
		}
		chunk.Sections[section.Y] = ChunkSection{
			Y:           section.Y,
			BlockStates: BlockPalette{Palette: blocks, Data: section.BlockStates.Data},
			Biomes:      BiomePalette{Palette: section.Biomes.Palette, Data: section.Biomes.Data},
			SkyLight:    append([]byte(nil), section.SkyLight...), BlockLight: append([]byte(nil), section.BlockLight...),
		}
	}
	return chunk, nil
}
