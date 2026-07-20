// SPDX-License-Identifier: AGPL-3.0-only

package protocol

import (
	"bytes"

	"github.com/Tnze/go-mc/nbt"
)

// EncodeNetworkNBT contains the third-party NBT dependency inside the wire
// adapter so session and gameplay packages depend only on Golem-owned APIs.
func EncodeNetworkNBT(value any) ([]byte, error) {
	var output bytes.Buffer
	encoder := nbt.NewEncoder(&output)
	encoder.NetworkFormat(true)
	if err := encoder.Encode(value, ""); err != nil {
		return nil, err
	}
	return output.Bytes(), nil
}
