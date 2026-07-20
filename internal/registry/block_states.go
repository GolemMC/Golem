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
	"math/bits"
	"sort"
	"strings"
	"sync"
)

//go:embed data/blocks_1.21.1.json.gz
var compressedBlocks []byte

type blockDefinition struct {
	ID           int32           `json:"id"`
	Name         string          `json:"name"`
	DisplayName  string          `json:"displayName"`
	DefaultState int32           `json:"defaultState"`
	MinStateID   int32           `json:"minStateId"`
	MaxStateID   int32           `json:"maxStateId"`
	States       []blockProperty `json:"states"`
	BoundingBox  string          `json:"boundingBox"`
}

type blockProperty struct {
	Name      string   `json:"name"`
	Type      string   `json:"type"`
	NumValues int      `json:"num_values"`
	Values    []string `json:"values"`
}

type blockIndex struct {
	byName   map[string]blockDefinition
	ordered  []blockDefinition
	maxState int32
}

// BlockStateDefinition is a fully resolved vanilla block state.
type BlockStateDefinition struct {
	ID          int32
	Name        string
	DisplayName string
	Properties  map[string]string
	BoundingBox string
}

var (
	blocksOnce sync.Once
	blocksData blockIndex
	blocksErr  error
)

// BlockStateID resolves an Anvil palette name and property compound to the
// exact vanilla 1.21.1 global block-state ID.
func BlockStateID(name string, properties map[string]string) (int32, error) {
	index, err := loadBlocks()
	if err != nil {
		return 0, err
	}
	name = strings.TrimPrefix(name, "minecraft:")
	block, ok := index.byName[name]
	if !ok {
		return 0, fmt.Errorf("unknown 1.21.1 block %q", name)
	}
	if len(properties) != len(block.States) {
		return 0, fmt.Errorf("block %q has %d properties, expected %d", name, len(properties), len(block.States))
	}
	offset := int32(0)
	multiplier := int32(1)
	for i := len(block.States) - 1; i >= 0; i-- {
		property := block.States[i]
		value, ok := properties[property.Name]
		if !ok {
			return 0, fmt.Errorf("block %q is missing property %q", name, property.Name)
		}
		values := property.Values
		if property.Type == "bool" && len(values) == 0 {
			values = []string{"true", "false"}
		}
		valueIndex := -1
		for j, allowed := range values {
			if value == allowed {
				valueIndex = j
				break
			}
		}
		if valueIndex < 0 || len(values) != property.NumValues {
			return 0, fmt.Errorf("block %q property %q has invalid value %q", name, property.Name, value)
		}
		offset += int32(valueIndex) * multiplier
		multiplier *= int32(property.NumValues)
	}
	id := block.MinStateID + offset
	if id > block.MaxStateID {
		return 0, fmt.Errorf("computed state %d outside block %q range", id, name)
	}
	return id, nil
}

// DefaultBlockState returns the canonical vanilla state used when a block is
// created without contextual placement rules.
func DefaultBlockState(name string) (BlockStateDefinition, error) {
	index, err := loadBlocks()
	if err != nil {
		return BlockStateDefinition{}, err
	}
	name = strings.TrimPrefix(name, "minecraft:")
	block, ok := index.byName[name]
	if !ok {
		return BlockStateDefinition{}, fmt.Errorf("unknown 1.21.1 block %q", name)
	}
	properties, err := blockPropertiesForID(block, block.DefaultState)
	if err != nil {
		return BlockStateDefinition{}, err
	}
	return resolvedBlockState(block, block.DefaultState, properties), nil
}

// BlockStateByID resolves every vanilla 1.21.1 global state ID, including all
// property combinations rather than only each block's default.
func BlockStateByID(id int32) (BlockStateDefinition, error) {
	index, err := loadBlocks()
	if err != nil {
		return BlockStateDefinition{}, err
	}
	if id < 0 || id > index.maxState {
		return BlockStateDefinition{}, fmt.Errorf("block state ID %d outside 0..%d", id, index.maxState)
	}
	i := sort.Search(len(index.ordered), func(i int) bool { return index.ordered[i].MaxStateID >= id })
	if i == len(index.ordered) || id < index.ordered[i].MinStateID {
		return BlockStateDefinition{}, fmt.Errorf("block state ID %d is not assigned", id)
	}
	block := index.ordered[i]
	properties, err := blockPropertiesForID(block, id)
	if err != nil {
		return BlockStateDefinition{}, err
	}
	return resolvedBlockState(block, id, properties), nil
}

func resolvedBlockState(block blockDefinition, id int32, properties map[string]string) BlockStateDefinition {
	return BlockStateDefinition{ID: id, Name: "minecraft:" + block.Name, DisplayName: block.DisplayName, Properties: properties, BoundingBox: block.BoundingBox}
}

func blockPropertiesForID(block blockDefinition, id int32) (map[string]string, error) {
	if id < block.MinStateID || id > block.MaxStateID {
		return nil, fmt.Errorf("state ID %d outside block %q range", id, block.Name)
	}
	if len(block.States) == 0 {
		return nil, nil
	}
	offset := id - block.MinStateID
	properties := make(map[string]string, len(block.States))
	for i := len(block.States) - 1; i >= 0; i-- {
		property := block.States[i]
		if property.NumValues < 1 {
			return nil, fmt.Errorf("block %q property %q has no values", block.Name, property.Name)
		}
		values := property.Values
		if property.Type == "bool" && len(values) == 0 {
			values = []string{"true", "false"}
		}
		valueIndex := int(offset % int32(property.NumValues))
		offset /= int32(property.NumValues)
		if valueIndex >= len(values) {
			return nil, fmt.Errorf("block %q property %q value index %d is unavailable", block.Name, property.Name, valueIndex)
		}
		properties[property.Name] = values[valueIndex]
	}
	if offset != 0 {
		return nil, fmt.Errorf("state ID %d overflows block %q properties", id, block.Name)
	}
	return properties, nil
}

func BlockStateBits() (uint8, error) {
	index, err := loadBlocks()
	if err != nil {
		return 0, err
	}
	return uint8(bits.Len32(uint32(index.maxState))), nil
}

func BlockCount() (int, error) {
	index, err := loadBlocks()
	return len(index.byName), err
}

func BlockStateCount() (int, error) {
	index, err := loadBlocks()
	return int(index.maxState) + 1, err
}

func loadBlocks() (blockIndex, error) {
	blocksOnce.Do(func() {
		zr, err := gzip.NewReader(bytes.NewReader(compressedBlocks))
		if err != nil {
			blocksErr = err
			return
		}
		data, err := io.ReadAll(io.LimitReader(zr, 4<<20))
		closeErr := zr.Close()
		if err != nil {
			blocksErr = err
			return
		}
		if closeErr != nil {
			blocksErr = closeErr
			return
		}
		if fmt.Sprintf("%x", sha256.Sum256(data)) != BlockFixtureSHA256 {
			blocksErr = fmt.Errorf("embedded block-state fixture checksum mismatch")
			return
		}
		var blocks []blockDefinition
		if err := json.Unmarshal(data, &blocks); err != nil {
			blocksErr = err
			return
		}
		blocksData.byName = make(map[string]blockDefinition, len(blocks))
		blocksData.ordered = append([]blockDefinition(nil), blocks...)
		sort.Slice(blocksData.ordered, func(i, j int) bool { return blocksData.ordered[i].MinStateID < blocksData.ordered[j].MinStateID })
		for _, block := range blocks {
			if _, exists := blocksData.byName[block.Name]; exists {
				blocksErr = fmt.Errorf("duplicate block definition %q", block.Name)
				return
			}
			blocksData.byName[block.Name] = block
			if block.MaxStateID > blocksData.maxState {
				blocksData.maxState = block.MaxStateID
			}
			if block.DefaultState < block.MinStateID || block.DefaultState > block.MaxStateID {
				blocksErr = fmt.Errorf("block %q default state is outside its range", block.Name)
				return
			}
		}
		if len(blocksData.byName) != 1060 || blocksData.maxState != 26683 {
			blocksErr = fmt.Errorf("unexpected 1.21.1 block registry dimensions")
		}
	})
	return blocksData, blocksErr
}
