// SPDX-License-Identifier: AGPL-3.0-only

// Package registry owns the reviewed, version-specific dynamic registry data.
package registry

import (
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	_ "embed"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"sort"
	"sync"

	"github.com/GolemMC/Golem/internal/protocol"
)

//go:embed data/login_packet_1.21.1.json.gz
var compressedFixture []byte

var (
	fixtureOnce sync.Once
	fixtureData fixture
	fixtureErr  error
)

type fixture struct {
	DimensionCodec map[string]registryData `json:"dimensionCodec"`
}
type registryData struct {
	ID      string  `json:"id"`
	Entries []entry `json:"entries"`
}
type entry struct {
	Key   string `json:"key"`
	Value *node  `json:"value"`
}
type node struct {
	Type  string          `json:"type"`
	Value json.RawMessage `json:"value"`
}

// ConfigurationPayloads returns registry-data packet payloads in stable registry-ID order.
// When omitData is true, entry keys and numeric ordering are retained while vanilla NBT is omitted.
func ConfigurationPayloads(omitData bool) ([][]byte, error) {
	f, err := loadFixture()
	if err != nil {
		return nil, err
	}
	ids := make([]string, 0, len(f.DimensionCodec))
	for id := range f.DimensionCodec {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	packets := make([][]byte, 0, len(ids))
	for _, id := range ids {
		reg := f.DimensionCodec[id]
		if reg.ID != id {
			return nil, fmt.Errorf("registry key %q contains id %q", id, reg.ID)
		}
		var out protocol.Encoder
		out.String(id)
		out.VarInt(int32(len(reg.Entries)))
		for _, item := range reg.Entries {
			out.String(item.Key)
			hasData := !omitData && item.Value != nil
			out.Bool(hasData)
			if hasData {
				if err := writeNetworkNBT(&out.Buffer, *item.Value); err != nil {
					return nil, fmt.Errorf("encode %s entry %s: %w", id, item.Key, err)
				}
			}
		}
		packets = append(packets, append([]byte(nil), out.Bytes()...))
	}
	return packets, nil
}

func loadFixture() (fixture, error) {
	fixtureOnce.Do(func() {
		zr, err := gzip.NewReader(bytes.NewReader(compressedFixture))
		if err != nil {
			fixtureErr = err
			return
		}
		b, err := io.ReadAll(io.LimitReader(zr, 2<<20))
		closeErr := zr.Close()
		if err != nil {
			fixtureErr = err
			return
		}
		if closeErr != nil {
			fixtureErr = closeErr
			return
		}
		if fmt.Sprintf("%x", sha256.Sum256(b)) != FixtureSHA256 {
			fixtureErr = fmt.Errorf("embedded registry fixture checksum mismatch")
			return
		}
		if err := json.Unmarshal(b, &fixtureData); err != nil {
			fixtureErr = err
			return
		}
		if len(fixtureData.DimensionCodec) != 11 {
			fixtureErr = fmt.Errorf("registry fixture contains %d registries; expected 11", len(fixtureData.DimensionCodec))
		}
	})
	return fixtureData, fixtureErr
}

func EntryID(registryID, key string) (int32, error) {
	f, err := loadFixture()
	if err != nil {
		return 0, err
	}
	reg, ok := f.DimensionCodec[registryID]
	if !ok {
		return 0, fmt.Errorf("unknown registry %q", registryID)
	}
	for id, item := range reg.Entries {
		if item.Key == key {
			return int32(id), nil
		}
	}
	return 0, fmt.Errorf("unknown %s entry %q", registryID, key)
}

func EntryCount(registryID string) (int, error) {
	f, err := loadFixture()
	if err != nil {
		return 0, err
	}
	reg, ok := f.DimensionCodec[registryID]
	if !ok {
		return 0, fmt.Errorf("unknown registry %q", registryID)
	}
	return len(reg.Entries), nil
}

func writeNetworkNBT(w io.Writer, n node) error {
	t, err := tagID(n.Type)
	if err != nil {
		return err
	}
	if _, err := w.Write([]byte{t}); err != nil {
		return err
	}
	return writePayload(w, n.Type, n.Value)
}

func writePayload(w io.Writer, typ string, raw json.RawMessage) error {
	switch typ {
	case "byte":
		var v int8
		if err := json.Unmarshal(raw, &v); err != nil {
			return err
		}
		_, err := w.Write([]byte{byte(v)})
		return err
	case "short":
		var v int16
		if err := json.Unmarshal(raw, &v); err != nil {
			return err
		}
		return binary.Write(w, binary.BigEndian, v)
	case "int":
		var v int32
		if err := json.Unmarshal(raw, &v); err != nil {
			return err
		}
		return binary.Write(w, binary.BigEndian, v)
	case "long":
		v, err := prismarineLong(raw)
		if err != nil {
			return err
		}
		return binary.Write(w, binary.BigEndian, v)
	case "float":
		var v float32
		if err := json.Unmarshal(raw, &v); err != nil {
			return err
		}
		return binary.Write(w, binary.BigEndian, math.Float32bits(v))
	case "double":
		var v float64
		if err := json.Unmarshal(raw, &v); err != nil {
			return err
		}
		return binary.Write(w, binary.BigEndian, math.Float64bits(v))
	case "string":
		var v string
		if err := json.Unmarshal(raw, &v); err != nil {
			return err
		}
		return writeNBTString(w, v)
	case "compound":
		var fields map[string]node
		if err := json.Unmarshal(raw, &fields); err != nil {
			return err
		}
		names := make([]string, 0, len(fields))
		for name := range fields {
			names = append(names, name)
		}
		sort.Strings(names)
		for _, name := range names {
			child := fields[name]
			id, err := tagID(child.Type)
			if err != nil {
				return err
			}
			if _, err := w.Write([]byte{id}); err != nil {
				return err
			}
			if err := writeNBTString(w, name); err != nil {
				return err
			}
			if err := writePayload(w, child.Type, child.Value); err != nil {
				return err
			}
		}
		_, err := w.Write([]byte{0})
		return err
	case "list":
		var list struct {
			Type  string            `json:"type"`
			Value []json.RawMessage `json:"value"`
		}
		if string(raw) == "{}" {
			_, err := w.Write([]byte{0, 0, 0, 0, 0})
			return err
		}
		if err := json.Unmarshal(raw, &list); err != nil {
			return err
		}
		id, err := tagID(list.Type)
		if err != nil {
			return err
		}
		if _, err := w.Write([]byte{id}); err != nil {
			return err
		}
		if err := binary.Write(w, binary.BigEndian, int32(len(list.Value))); err != nil {
			return err
		}
		for _, value := range list.Value {
			if err := writePayload(w, list.Type, value); err != nil {
				return err
			}
		}
		return nil
	case "intArray":
		var values []int32
		if err := json.Unmarshal(raw, &values); err != nil {
			return err
		}
		if err := binary.Write(w, binary.BigEndian, int32(len(values))); err != nil {
			return err
		}
		for _, v := range values {
			if err := binary.Write(w, binary.BigEndian, v); err != nil {
				return err
			}
		}
		return nil
	case "longArray":
		var values []json.RawMessage
		if err := json.Unmarshal(raw, &values); err != nil {
			return err
		}
		if err := binary.Write(w, binary.BigEndian, int32(len(values))); err != nil {
			return err
		}
		for _, rawLong := range values {
			v, err := prismarineLong(rawLong)
			if err != nil {
				return err
			}
			if err := binary.Write(w, binary.BigEndian, v); err != nil {
				return err
			}
		}
		return nil
	default:
		return fmt.Errorf("unsupported NBT type %q", typ)
	}
}

func prismarineLong(raw json.RawMessage) (int64, error) {
	var parts [2]int64
	if err := json.Unmarshal(raw, &parts); err != nil {
		return 0, err
	}
	return int64(uint64(uint32(parts[0]))<<32 | uint64(uint32(parts[1]))), nil
}

func writeNBTString(w io.Writer, value string) error {
	if len(value) > math.MaxUint16 {
		return fmt.Errorf("NBT string too long")
	}
	if err := binary.Write(w, binary.BigEndian, uint16(len(value))); err != nil {
		return err
	}
	_, err := io.WriteString(w, value)
	return err
}

func tagID(name string) (byte, error) {
	ids := map[string]byte{"end": 0, "byte": 1, "short": 2, "int": 3, "long": 4, "float": 5, "double": 6, "byteArray": 7, "string": 8, "list": 9, "compound": 10, "intArray": 11, "longArray": 12}
	id, ok := ids[name]
	if !ok {
		return 0, fmt.Errorf("unknown NBT type %q", name)
	}
	return id, nil
}
