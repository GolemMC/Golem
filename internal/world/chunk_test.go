// SPDX-License-Identifier: AGPL-3.0-only

package world

import (
	"bytes"
	"testing"

	"github.com/GolemMC/Golem/internal/version"
	"github.com/Tnze/go-mc/nbt"
)

func TestDecodeChunk1211(t *testing.T) {
	disk := diskChunk{
		DataVersion: version.WorldDataVersion,
		XPos:        -2,
		ZPos:        3,
		Heightmaps:  map[string][]int64{"WORLD_SURFACE": make([]int64, 37)},
		Sections: []diskSection{{
			Y:           0,
			BlockStates: diskBlockState{Palette: []diskPaletteEntry{{Name: "minecraft:stone"}}},
			Biomes:      diskBiome{Palette: []string{"minecraft:plains"}},
			SkyLight:    bytes.Repeat([]byte{0xff}, 2048),
		}},
	}
	data, err := nbt.Marshal(disk)
	if err != nil {
		t.Fatal(err)
	}
	chunk, err := DecodeChunk(data, -2, 3)
	if err != nil {
		t.Fatal(err)
	}
	section := chunk.Sections[0]
	if len(section.BlockStates.Palette) != 1 || section.BlockStates.Palette[0].Name != "minecraft:stone" {
		t.Fatalf("unexpected block palette: %#v", section.BlockStates.Palette)
	}
	if len(section.SkyLight) != 2048 || len(chunk.Heightmaps["WORLD_SURFACE"]) != 37 {
		t.Fatal("chunk light or heightmap was not retained")
	}
}

func TestDecodeChunkRejectsCoordinateMismatch(t *testing.T) {
	data, err := nbt.Marshal(diskChunk{DataVersion: version.WorldDataVersion, XPos: 1, ZPos: 2})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := DecodeChunk(data, 9, 2); err == nil {
		t.Fatal("coordinate mismatch was accepted")
	}
}
