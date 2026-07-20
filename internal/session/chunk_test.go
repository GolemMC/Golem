// SPDX-License-Identifier: AGPL-3.0-only

package session

import (
	"testing"

	"github.com/GolemMC/Golem/internal/world"
)

func TestEncodeRealWorldChunkPalette(t *testing.T) {
	chunk := world.Chunk{
		X: 2, Z: -3,
		Sections: map[int8]world.ChunkSection{0: {
			Y:           0,
			BlockStates: world.BlockPalette{Palette: []world.BlockState{{Name: "minecraft:stone"}}},
			Biomes:      world.BiomePalette{Palette: []string{"minecraft:plains"}},
		}},
		Heightmaps: map[string][]int64{},
	}
	payload, err := encodeWorldChunk(chunk)
	if err != nil {
		t.Fatal(err)
	}
	if len(payload) == 0 {
		t.Fatal("empty chunk packet")
	}
}

func TestPaletteRejectsOutOfRangeIndex(t *testing.T) {
	if _, err := unpackPalette([]int64{15}, 2, 1, 4); err == nil {
		t.Fatal("out-of-range palette index accepted")
	}
}
