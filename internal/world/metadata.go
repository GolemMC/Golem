// SPDX-License-Identifier: AGPL-3.0-only

package world

import (
	"compress/gzip"
	"fmt"
	"os"

	"github.com/GolemMC/Golem/internal/version"
	"github.com/Tnze/go-mc/nbt"
)

type Metadata struct {
	DataVersion int32
	LevelName   string
	Spawn       Position
}

type Position struct{ X, Y, Z int32 }

type levelRoot struct {
	Data nbt.RawMessage `nbt:"Data"`
}
type levelData struct {
	DataVersion int32  `nbt:"DataVersion"`
	LevelName   string `nbt:"LevelName"`
	SpawnX      int32  `nbt:"SpawnX"`
	SpawnY      int32  `nbt:"SpawnY"`
	SpawnZ      int32  `nbt:"SpawnZ"`
}

func ReadMetadata(path string) (Metadata, error) {
	f, err := os.Open(path)
	if err != nil {
		return Metadata{}, fmt.Errorf("open level metadata %q: %w", path, err)
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return Metadata{}, fmt.Errorf("decompress level metadata %q: %w", path, err)
	}
	defer gz.Close()
	var root levelRoot
	if _, err := nbt.NewDecoder(gz).Decode(&root); err != nil {
		return Metadata{}, fmt.Errorf("decode level metadata %q: %w", path, err)
	}
	var data levelData
	if err := root.Data.Unmarshal(&data); err != nil {
		return Metadata{}, fmt.Errorf("decode Data compound in %q: %w", path, err)
	}
	return Metadata{DataVersion: data.DataVersion, LevelName: data.LevelName, Spawn: Position{data.SpawnX, data.SpawnY, data.SpawnZ}}, nil
}

func ValidateMetadata(meta Metadata) error {
	if meta.DataVersion != version.WorldDataVersion {
		return fmt.Errorf("unsupported world data version %d; Golem supports Minecraft %s data version %d only (world was not modified)", meta.DataVersion, version.MinecraftVersion, version.WorldDataVersion)
	}
	return nil
}
