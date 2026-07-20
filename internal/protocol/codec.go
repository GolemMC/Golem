// SPDX-License-Identifier: AGPL-3.0-only

package protocol

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"io"
	"math"
)

const (
	MaxStringBytes    = 32767 * 4
	MaxByteArrayBytes = 1 << 20
)

type Decoder struct{ r *bytes.Reader }

func NewDecoder(b []byte) *Decoder        { return &Decoder{r: bytes.NewReader(b)} }
func (d *Decoder) Remaining() int         { return d.r.Len() }
func (d *Decoder) VarInt() (int32, error) { return ReadVarInt(d.r) }
func (d *Decoder) Bool() (bool, error)    { b, err := d.Byte(); return b != 0, err }
func (d *Decoder) Byte() (byte, error)    { b, err := d.r.ReadByte(); return b, err }
func (d *Decoder) Uint16() (uint16, error) {
	var v uint16
	err := binary.Read(d.r, binary.BigEndian, &v)
	return v, err
}
func (d *Decoder) Int16() (int16, error) {
	var v int16
	err := binary.Read(d.r, binary.BigEndian, &v)
	return v, err
}
func (d *Decoder) Int32() (int32, error) {
	var v int32
	err := binary.Read(d.r, binary.BigEndian, &v)
	return v, err
}
func (d *Decoder) Int64() (int64, error) {
	var v int64
	err := binary.Read(d.r, binary.BigEndian, &v)
	return v, err
}
func (d *Decoder) Position() (x, y, z int32, err error) {
	packed, err := d.Int64()
	if err != nil {
		return 0, 0, 0, err
	}
	value := uint64(packed)
	x = int32(value >> 38)
	z = int32((value >> 12) & 0x3ffffff)
	y = int32(value & 0xfff)
	if x >= 1<<25 {
		x -= 1 << 26
	}
	if z >= 1<<25 {
		z -= 1 << 26
	}
	if y >= 1<<11 {
		y -= 1 << 12
	}
	return x, y, z, nil
}
func (d *Decoder) Float32() (float32, error) {
	var v uint32
	if err := binary.Read(d.r, binary.BigEndian, &v); err != nil {
		return 0, err
	}
	return math.Float32frombits(v), nil
}
func (d *Decoder) Float64() (float64, error) {
	var v uint64
	if err := binary.Read(d.r, binary.BigEndian, &v); err != nil {
		return 0, err
	}
	return math.Float64frombits(v), nil
}

func (d *Decoder) String(maxChars int) (string, error) {
	n, err := d.VarInt()
	if err != nil {
		return "", err
	}
	maxBytes := maxChars * 4
	if maxBytes > MaxStringBytes {
		maxBytes = MaxStringBytes
	}
	l, err := length(n, maxBytes)
	if err != nil {
		return "", fmt.Errorf("string: %w", err)
	}
	b := make([]byte, l)
	if _, err := io.ReadFull(d.r, b); err != nil {
		return "", err
	}
	if len([]rune(string(b))) > maxChars {
		return "", fmt.Errorf("string exceeds %d characters", maxChars)
	}
	return string(b), nil
}

func (d *Decoder) ByteArray(max int) ([]byte, error) {
	n, err := d.VarInt()
	if err != nil {
		return nil, err
	}
	l, err := length(n, max)
	if err != nil {
		return nil, fmt.Errorf("byte array: %w", err)
	}
	b := make([]byte, l)
	_, err = io.ReadFull(d.r, b)
	return b, err
}

func (d *Decoder) Bytes(n int) ([]byte, error) {
	if n < 0 || n > d.r.Len() {
		return nil, io.ErrUnexpectedEOF
	}
	b := make([]byte, n)
	_, err := io.ReadFull(d.r, b)
	return b, err
}

type Encoder struct{ bytes.Buffer }

func (e *Encoder) VarInt(v int32) { e.Write(AppendVarInt(nil, v)) }
func (e *Encoder) Bool(v bool) {
	if v {
		e.WriteByte(1)
	} else {
		e.WriteByte(0)
	}
}
func (e *Encoder) Uint16(v uint16) { var b [2]byte; binary.BigEndian.PutUint16(b[:], v); e.Write(b[:]) }
func (e *Encoder) Int32(v int32) {
	var b [4]byte
	binary.BigEndian.PutUint32(b[:], uint32(v))
	e.Write(b[:])
}
func (e *Encoder) Int16(v int16) {
	var b [2]byte
	binary.BigEndian.PutUint16(b[:], uint16(v))
	e.Write(b[:])
}
func (e *Encoder) Int64(v int64) {
	var b [8]byte
	binary.BigEndian.PutUint64(b[:], uint64(v))
	e.Write(b[:])
}
func (e *Encoder) Float32(v float32)  { e.Int32(int32(math.Float32bits(v))) }
func (e *Encoder) Float64(v float64)  { e.Int64(int64(math.Float64bits(v))) }
func (e *Encoder) String(v string)    { e.VarInt(int32(len(v))); e.WriteString(v) }
func (e *Encoder) ByteArray(v []byte) { e.VarInt(int32(len(v))); e.Write(v) }
func (e *Encoder) UUID(v [16]byte)    { e.Write(v[:]) }
func (e *Encoder) Position(x, y, z int32) {
	packed := uint64(uint32(x)&0x3ffffff)<<38 | uint64(uint32(z)&0x3ffffff)<<12 | uint64(uint32(y)&0xfff)
	e.Int64(int64(packed))
}
