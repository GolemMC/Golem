// SPDX-License-Identifier: AGPL-3.0-only

package protocol

import "testing"

func TestPositionRoundTrip(t *testing.T) {
	cases := [][3]int32{
		{0, 0, 0},
		{30_000_000, 319, -30_000_000},
		{-1, -64, -1},
		{33_554_431, 2_047, -33_554_432},
	}
	for _, test := range cases {
		var encoded Encoder
		encoded.Position(test[0], test[1], test[2])
		x, y, z, err := NewDecoder(encoded.Bytes()).Position()
		if err != nil {
			t.Fatal(err)
		}
		if got := [3]int32{x, y, z}; got != test {
			t.Fatalf("Position%v decoded as %v", test, got)
		}
	}
}
