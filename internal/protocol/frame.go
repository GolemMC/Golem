// SPDX-License-Identifier: AGPL-3.0-only

package protocol

import (
	"bytes"
	"compress/zlib"
	"fmt"
	"io"
)

type FrameCodec struct {
	MaxPacketBytes       int
	CompressionThreshold int
}

func (c FrameCodec) Read(r io.Reader) (packetID int32, payload []byte, err error) {
	if c.MaxPacketBytes <= 0 {
		return 0, nil, fmt.Errorf("invalid maximum packet size")
	}
	n, err := ReadVarInt(r)
	if err != nil {
		return 0, nil, fmt.Errorf("read frame length: %w", err)
	}
	l, err := length(n, c.MaxPacketBytes+5)
	if err != nil {
		return 0, nil, fmt.Errorf("frame: %w", err)
	}
	frame := make([]byte, l)
	if _, err := io.ReadFull(r, frame); err != nil {
		return 0, nil, fmt.Errorf("read frame body: %w", err)
	}
	if c.CompressionThreshold >= 0 {
		frame, err = c.decompress(frame)
		if err != nil {
			return 0, nil, err
		}
	}
	d := NewDecoder(frame)
	packetID, err = d.VarInt()
	if err != nil {
		return 0, nil, fmt.Errorf("read packet id: %w", err)
	}
	payload, err = d.Bytes(d.Remaining())
	return packetID, payload, err
}

func (c FrameCodec) Write(w io.Writer, packetID int32, payload []byte) error {
	packet := AppendVarInt(nil, packetID)
	packet = append(packet, payload...)
	if len(packet) > c.MaxPacketBytes {
		return fmt.Errorf("packet length %d exceeds limit %d", len(packet), c.MaxPacketBytes)
	}
	frame := packet
	if c.CompressionThreshold >= 0 {
		var body bytes.Buffer
		if len(packet) >= c.CompressionThreshold {
			body.Write(AppendVarInt(nil, int32(len(packet))))
			zw := zlib.NewWriter(&body)
			if _, err := zw.Write(packet); err != nil {
				return err
			}
			if err := zw.Close(); err != nil {
				return err
			}
		} else {
			body.WriteByte(0)
			body.Write(packet)
		}
		frame = body.Bytes()
	}
	header := AppendVarInt(nil, int32(len(frame)))
	if _, err := w.Write(header); err != nil {
		return err
	}
	_, err := w.Write(frame)
	return err
}

func (c FrameCodec) decompress(frame []byte) ([]byte, error) {
	d := NewDecoder(frame)
	n, err := d.VarInt()
	if err != nil {
		return nil, fmt.Errorf("compressed frame length: %w", err)
	}
	if n == 0 {
		return d.Bytes(d.Remaining())
	}
	want, err := length(n, c.MaxPacketBytes)
	if err != nil {
		return nil, fmt.Errorf("uncompressed frame: %w", err)
	}
	if want < c.CompressionThreshold {
		return nil, fmt.Errorf("compressed packet size %d is below threshold %d", want, c.CompressionThreshold)
	}
	compressed, err := d.Bytes(d.Remaining())
	if err != nil {
		return nil, err
	}
	zr, err := zlib.NewReader(bytes.NewReader(compressed))
	if err != nil {
		return nil, fmt.Errorf("open zlib stream: %w", err)
	}
	defer zr.Close()
	out, err := io.ReadAll(io.LimitReader(zr, int64(want)+1))
	if err != nil {
		return nil, fmt.Errorf("decompress packet: %w", err)
	}
	if len(out) != want {
		return nil, fmt.Errorf("decompressed length %d does not match declared length %d", len(out), want)
	}
	return out, nil
}
