// SPDX-License-Identifier: AGPL-3.0-only

package registry

import (
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	_ "embed"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"sync"
)

const (
	PlayerEntityTypeID = int32(128)
)

//go:embed data/entities_1.21.1.json.gz
var compressedEntities []byte

// EntityDefinition describes a vanilla 1.21.1 entity registry entry. Metadata
// keys are ordered exactly as the protocol's inherited metadata table.
type EntityDefinition struct {
	ID           int32    `json:"id"`
	InternalID   int32    `json:"internalId"`
	Name         string   `json:"name"`
	DisplayName  string   `json:"displayName"`
	Width        float64  `json:"width"`
	Height       float64  `json:"height"`
	Type         string   `json:"type"`
	Category     string   `json:"category"`
	MetadataKeys []string `json:"metadataKeys"`
}

type entityIndex struct {
	byID   []EntityDefinition
	byName map[string]EntityDefinition
}

var (
	entitiesOnce sync.Once
	entitiesData entityIndex
	entitiesErr  error
)

func EntityByID(id int32) (EntityDefinition, bool, error) {
	index, err := loadEntities()
	if err != nil {
		return EntityDefinition{}, false, err
	}
	if id < 0 || int(id) >= len(index.byID) {
		return EntityDefinition{}, false, nil
	}
	return cloneEntity(index.byID[id]), true, nil
}

func EntityByName(name string) (EntityDefinition, bool, error) {
	index, err := loadEntities()
	if err != nil {
		return EntityDefinition{}, false, err
	}
	name = strings.TrimPrefix(name, "minecraft:")
	entity, ok := index.byName[name]
	if !ok {
		return EntityDefinition{}, false, nil
	}
	return cloneEntity(entity), true, nil
}

func EntityCount() (int, error) {
	index, err := loadEntities()
	return len(index.byID), err
}

func cloneEntity(entity EntityDefinition) EntityDefinition {
	entity.MetadataKeys = append([]string(nil), entity.MetadataKeys...)
	return entity
}

func loadEntities() (entityIndex, error) {
	entitiesOnce.Do(func() {
		zr, err := gzip.NewReader(bytes.NewReader(compressedEntities))
		if err != nil {
			entitiesErr = err
			return
		}
		data, err := io.ReadAll(io.LimitReader(zr, 4<<20))
		closeErr := zr.Close()
		if err != nil {
			entitiesErr = err
			return
		}
		if closeErr != nil {
			entitiesErr = closeErr
			return
		}
		if fmt.Sprintf("%x", sha256.Sum256(data)) != EntityFixtureSHA256 {
			entitiesErr = fmt.Errorf("embedded entity fixture checksum mismatch")
			return
		}
		var entities []EntityDefinition
		if err := json.Unmarshal(data, &entities); err != nil {
			entitiesErr = err
			return
		}
		if len(entities) != 130 {
			entitiesErr = fmt.Errorf("unexpected 1.21.1 entity count %d", len(entities))
			return
		}
		entitiesData.byID = make([]EntityDefinition, len(entities))
		entitiesData.byName = make(map[string]EntityDefinition, len(entities))
		for _, entity := range entities {
			if entity.ID < 0 || int(entity.ID) >= len(entities) || entity.InternalID != entity.ID {
				entitiesErr = fmt.Errorf("entity %q has invalid IDs %d/%d", entity.Name, entity.ID, entity.InternalID)
				return
			}
			if entitiesData.byID[entity.ID].Name != "" {
				entitiesErr = fmt.Errorf("duplicate entity ID %d", entity.ID)
				return
			}
			if _, exists := entitiesData.byName[entity.Name]; exists {
				entitiesErr = fmt.Errorf("duplicate entity name %q", entity.Name)
				return
			}
			entitiesData.byID[entity.ID] = entity
			entitiesData.byName[entity.Name] = entity
		}
		player := entitiesData.byID[PlayerEntityTypeID]
		if player.Name != "player" {
			entitiesErr = fmt.Errorf("entity ID %d is %q, expected player", PlayerEntityTypeID, player.Name)
		}
	})
	return entitiesData, entitiesErr
}
