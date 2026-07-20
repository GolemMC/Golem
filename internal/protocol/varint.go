// SPDX-License-Identifier: AGPL-3.0-only

// Package protocol implements bounded Minecraft Java protocol primitives.
package protocol

import (
	"errors"
	"fmt"
	"io"
)

var (
	ErrVarIntTooLong  = errors.New("VarInt exceeds 5 bytes")
	ErrNegativeLength = errors.New("negative length")
)

func ReadVarInt(r io.Reader) (int32, error) {
	var value uint32
	var one [1]byte
	for i := 0; i < 5; i++ {
		if _, err := io.ReadFull(r, one[:]); err != nil {
			return 0, err
		}
		value |= uint32(one[0]&0x7f) << (7 * i)
		if one[0]&0x80 == 0 {
			return int32(value), nil
		}
	}
	return 0, ErrVarIntTooLong
}

func AppendVarInt(dst []byte, v int32) []byte {
	u := uint32(v)
	for {
		if u&^uint32(0x7f) == 0 {
			return append(dst, byte(u))
		}
		dst = append(dst, byte(u&0x7f)|0x80)
		u >>= 7
	}
}

func VarIntLen(v int32) int {
	n := 1
	for u := uint32(v); u&^uint32(0x7f) != 0; u >>= 7 {
		n++
	}
	return n
}

func length(v int32, max int) (int, error) {
	if v < 0 {
		return 0, ErrNegativeLength
	}
	if int64(v) > int64(max) {
		return 0, fmt.Errorf("length %d exceeds limit %d", v, max)
	}
	return int(v), nil
}
