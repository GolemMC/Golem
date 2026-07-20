// SPDX-License-Identifier: AGPL-3.0-only

package world

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"strings"

	"github.com/GolemMC/Golem/internal/version"
	"github.com/GolemMC/Golem/internal/world/region"
	"github.com/Tnze/go-mc/nbt"
)

const (
	maxEntitiesPerChunk = 4096
	maxPassengerDepth   = 16
)

// Entity is the version-independent portion of an existing vanilla entity
// needed for generic client spawning. Raw retains type-specific NBT read-only.
type Entity struct {
	UUID     [16]byte
	Type     string
	Position [3]float64
	Rotation [2]float32
	Velocity [3]float64
	OnGround bool
	Vehicle  [16]byte
	Raw      map[string]any
}

func (w *World) LoadEntities(chunkX, chunkZ int32) ([]Entity, error) {
	regionX, localX := region.WorldToRegion(chunkX)
	regionZ, localZ := region.WorldToRegion(chunkZ)
	path := region.EntityRegionPath(w.Path, regionX, regionZ)
	raw, err := region.ReadChunk(path, int(localX), int(localZ))
	if err != nil {
		if errors.Is(err, region.ErrChunkMissing) {
			return nil, nil
		}
		return nil, fmt.Errorf("read entity chunk (%d,%d): %w", chunkX, chunkZ, err)
	}
	var root map[string]any
	if _, err := nbt.NewDecoder(bytes.NewReader(raw)).Decode(&root); err != nil {
		return nil, fmt.Errorf("decode entity chunk (%d,%d): %w", chunkX, chunkZ, err)
	}
	dataVersion, ok := root["DataVersion"].(int32)
	if !ok || dataVersion != version.WorldDataVersion {
		return nil, fmt.Errorf("entity chunk data version %v is not supported", root["DataVersion"])
	}
	position, exists := root["Position"]
	coordinates, ok := position.([]int32)
	if !exists || !ok || len(coordinates) != 2 || coordinates[0] != chunkX || coordinates[1] != chunkZ {
		return nil, fmt.Errorf("entity chunk stored position is missing, malformed, or mismatched")
	}
	rawEntities, ok := root["Entities"].([]any)
	if !ok {
		return nil, fmt.Errorf("entity chunk Entities list is missing or malformed")
	}
	if len(rawEntities) > maxEntitiesPerChunk {
		return nil, fmt.Errorf("entity chunk exceeds %d root entities", maxEntitiesPerChunk)
	}
	entities := make([]Entity, 0, len(rawEntities))
	seen := make(map[[16]byte]struct{}, len(rawEntities))
	for _, rawEntity := range rawEntities {
		if err := appendDiskEntity(&entities, seen, rawEntity, 0); err != nil {
			return nil, fmt.Errorf("entity chunk (%d,%d): %w", chunkX, chunkZ, err)
		}
		if len(entities) > maxEntitiesPerChunk {
			return nil, fmt.Errorf("entity chunk exceeds %d entities including passengers", maxEntitiesPerChunk)
		}
	}
	return entities, nil
}

func appendDiskEntity(out *[]Entity, seen map[[16]byte]struct{}, value any, depth int) error {
	return appendDiskEntityWithVehicle(out, seen, value, depth, [16]byte{})
}

func appendDiskEntityWithVehicle(out *[]Entity, seen map[[16]byte]struct{}, value any, depth int, vehicle [16]byte) error {
	if depth > maxPassengerDepth {
		return fmt.Errorf("passenger nesting exceeds %d", maxPassengerDepth)
	}
	if len(*out) >= maxEntitiesPerChunk {
		return fmt.Errorf("entity chunk exceeds %d entities including passengers", maxEntitiesPerChunk)
	}
	raw, ok := value.(map[string]any)
	if !ok {
		return fmt.Errorf("entity is not a compound")
	}
	typeName, ok := raw["id"].(string)
	if !ok || typeName == "" || len(typeName) > 128 {
		return fmt.Errorf("entity has invalid id")
	}
	if !strings.Contains(typeName, ":") {
		typeName = "minecraft:" + typeName
	}
	id, err := entityUUID(raw["UUID"])
	if err != nil {
		return err
	}
	if _, duplicate := seen[id]; duplicate {
		return fmt.Errorf("duplicate entity UUID")
	}
	seen[id] = struct{}{}
	position, err := numericList[float64](raw["Pos"], 3)
	if err != nil {
		return fmt.Errorf("entity Pos: %w", err)
	}
	rotation, err := numericList[float32](raw["Rotation"], 2)
	if err != nil {
		return fmt.Errorf("entity Rotation: %w", err)
	}
	velocity := []float64{0, 0, 0}
	if rawMotion, exists := raw["Motion"]; exists {
		velocity, err = numericList[float64](rawMotion, 3)
		if err != nil {
			return fmt.Errorf("entity Motion: %w", err)
		}
	}
	for _, number := range append(append([]float64(nil), position...), velocity...) {
		if math.IsNaN(number) || math.IsInf(number, 0) || math.Abs(number) > 30_000_000 {
			return fmt.Errorf("entity has invalid numeric position or velocity")
		}
	}
	for _, angle := range rotation {
		if math.IsNaN(float64(angle)) || math.IsInf(float64(angle), 0) {
			return fmt.Errorf("entity has invalid rotation")
		}
	}
	onGround := false
	if rawGround, exists := raw["OnGround"]; exists {
		ground, ok := rawGround.(int8)
		if !ok || ground < 0 || ground > 1 {
			return fmt.Errorf("entity has invalid OnGround value")
		}
		onGround = ground != 0
	}
	*out = append(*out, Entity{
		UUID: id, Type: typeName,
		Position: [3]float64{position[0], position[1], position[2]},
		Rotation: [2]float32{rotation[0], rotation[1]},
		Velocity: [3]float64{velocity[0], velocity[1], velocity[2]},
		OnGround: onGround, Vehicle: vehicle, Raw: raw,
	})
	if passengers, exists := raw["Passengers"]; exists {
		list, ok := passengers.([]any)
		if !ok {
			return fmt.Errorf("entity Passengers is malformed")
		}
		for _, passenger := range list {
			if err := appendDiskEntityWithVehicle(out, seen, passenger, depth+1, id); err != nil {
				return err
			}
		}
	}
	return nil
}

func entityUUID(value any) ([16]byte, error) {
	var id [16]byte
	words, ok := value.([]int32)
	if !ok || len(words) != 4 {
		return id, fmt.Errorf("entity UUID is not a four-word int array")
	}
	for i, word := range words {
		binary.BigEndian.PutUint32(id[i*4:i*4+4], uint32(word))
	}
	if id == ([16]byte{}) {
		return id, fmt.Errorf("entity UUID is zero")
	}
	return id, nil
}
