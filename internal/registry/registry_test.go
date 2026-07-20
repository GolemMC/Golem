// SPDX-License-Identifier: AGPL-3.0-only

package registry

import (
	"bytes"
	"io"
	"testing"

	"github.com/GolemMC/Golem/internal/protocol"
	"github.com/Tnze/go-mc/nbt"
)

func TestConfigurationPayloads(t *testing.T) {
	full, err := ConfigurationPayloads(false)
	if err != nil {
		t.Fatal(err)
	}
	omitted, err := ConfigurationPayloads(true)
	if err != nil {
		t.Fatal(err)
	}
	if len(full) != 11 || len(omitted) != 11 {
		t.Fatalf("packet counts full=%d omitted=%d", len(full), len(omitted))
	}
	for i := range full {
		if len(full[i]) <= len(omitted[i]) {
			t.Fatalf("registry %d did not omit data", i)
		}
		validateRegistryPayload(t, full[i], true)
		validateRegistryPayload(t, omitted[i], false)
	}
}

func validateRegistryPayload(t *testing.T, payload []byte, expectData bool) {
	t.Helper()
	r := bytes.NewReader(payload)
	if _, err := readString(r); err != nil {
		t.Fatal(err)
	}
	count, err := protocol.ReadVarInt(r)
	if err != nil {
		t.Fatal(err)
	}
	for i := int32(0); i < count; i++ {
		if _, err := readString(r); err != nil {
			t.Fatal(err)
		}
		hasData, err := r.ReadByte()
		if err != nil {
			t.Fatal(err)
		}
		if (hasData != 0) != expectData {
			t.Fatalf("entry data=%t, expected %t", hasData != 0, expectData)
		}
		if hasData != 0 {
			var raw nbt.RawMessage
			dec := nbt.NewDecoder(r)
			dec.NetworkFormat(true)
			if _, err := dec.Decode(&raw); err != nil {
				t.Fatalf("invalid network NBT: %v", err)
			}
		}
	}
	if r.Len() != 0 {
		t.Fatalf("registry payload has %d trailing bytes", r.Len())
	}
}

func readString(r *bytes.Reader) (string, error) {
	n, err := protocol.ReadVarInt(r)
	if err != nil {
		return "", err
	}
	if n < 0 || int(n) > r.Len() {
		return "", io.ErrUnexpectedEOF
	}
	b := make([]byte, n)
	if _, err := io.ReadFull(r, b); err != nil {
		return "", err
	}
	return string(b), nil
}
