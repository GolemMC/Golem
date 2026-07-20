// SPDX-License-Identifier: AGPL-3.0-only

package protocol

import (
	"bytes"
	"errors"
	"math"
	"testing"
)

func TestVarIntRoundTrip(t *testing.T) {
	values := []int32{0, 1, 127, 128, 255, 2147483647, -1, math.MinInt32}
	for _, v := range values {
		b := AppendVarInt(nil, v)
		got, err := ReadVarInt(bytes.NewReader(b))
		if err != nil || got != v {
			t.Errorf("%d => %x => %d, %v", v, b, got, err)
		}
	}
}

func TestVarIntRejectsSixBytes(t *testing.T) {
	_, err := ReadVarInt(bytes.NewReader([]byte{0x80, 0x80, 0x80, 0x80, 0x80, 0}))
	if !errors.Is(err, ErrVarIntTooLong) {
		t.Fatalf("got %v", err)
	}
}

func FuzzVarInt(f *testing.F) {
	f.Add([]byte{0})
	f.Add([]byte{0xff, 0xff, 0xff, 0xff, 0x0f})
	f.Fuzz(func(t *testing.T, b []byte) { _, _ = ReadVarInt(bytes.NewReader(b)) })
}
