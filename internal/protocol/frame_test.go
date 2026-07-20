// SPDX-License-Identifier: AGPL-3.0-only

package protocol

import (
	"bytes"
	"strings"
	"testing"
)

func TestFrameRoundTrip(t *testing.T) {
	for _, threshold := range []int{-1, 0, 32} {
		c := FrameCodec{MaxPacketBytes: 1024, CompressionThreshold: threshold}
		var wire bytes.Buffer
		payload := []byte(strings.Repeat("x", 64))
		if err := c.Write(&wire, 42, payload); err != nil {
			t.Fatal(err)
		}
		id, got, err := c.Read(&wire)
		if err != nil {
			t.Fatal(err)
		}
		if id != 42 || !bytes.Equal(got, payload) {
			t.Fatalf("id=%d payload=%q", id, got)
		}
	}
}

func TestFrameLimit(t *testing.T) {
	c := FrameCodec{MaxPacketBytes: 8, CompressionThreshold: -1}
	var wire bytes.Buffer
	wire.Write(AppendVarInt(nil, 100))
	if _, _, err := c.Read(&wire); err == nil {
		t.Fatal("expected size error")
	}
}
