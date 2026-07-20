// SPDX-License-Identifier: AGPL-3.0-only

package session

import (
	"fmt"

	"github.com/GolemMC/Golem/internal/protocol"
)

func decodeCommandPacket(payload []byte, signed bool, maxLength int) (string, error) {
	d := protocol.NewDecoder(payload)
	command, err := d.String(maxLength)
	if err != nil {
		return "", err
	}
	if command == "" {
		return "", fmt.Errorf("command is empty")
	}
	if signed {
		if _, err := d.Int64(); err != nil {
			return "", err
		}
		if _, err := d.Int64(); err != nil {
			return "", err
		}
		count, err := d.VarInt()
		if err != nil || count < 0 || count > 8 {
			return "", fmt.Errorf("invalid command signature count %d", count)
		}
		for i := int32(0); i < count; i++ {
			if _, err := d.String(64); err != nil {
				return "", err
			}
			if _, err := d.Bytes(256); err != nil {
				return "", err
			}
		}
		messageCount, err := d.VarInt()
		if err != nil || messageCount < 0 {
			return "", fmt.Errorf("invalid command acknowledgement count")
		}
		if _, err := d.Bytes(3); err != nil {
			return "", err
		}
	}
	if d.Remaining() != 0 {
		return "", fmt.Errorf("command has %d trailing bytes", d.Remaining())
	}
	return command, nil
}
