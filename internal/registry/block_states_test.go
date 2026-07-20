// SPDX-License-Identifier: AGPL-3.0-only

package registry

import "testing"

func TestBlockStateIDsMatchVanilla1211(t *testing.T) {
	tests := []struct {
		name       string
		properties map[string]string
		want       int32
	}{
		{"minecraft:air", nil, 0},
		{"minecraft:stone", nil, 1},
		{"minecraft:water", map[string]string{"level": "0"}, 80},
		{"minecraft:oak_stairs", map[string]string{"facing": "north", "half": "bottom", "shape": "straight", "waterlogged": "false"}, 2885},
	}
	for _, test := range tests {
		got, err := BlockStateID(test.name, test.properties)
		if err != nil {
			t.Fatalf("%s: %v", test.name, err)
		}
		if got != test.want {
			t.Fatalf("%s state=%d want=%d", test.name, got, test.want)
		}
	}
	bits, err := BlockStateBits()
	if err != nil {
		t.Fatal(err)
	}
	if bits != 15 {
		t.Fatalf("global block-state width=%d want=15", bits)
	}
}

func TestBlockStateRejectsIncompleteProperties(t *testing.T) {
	if _, err := BlockStateID("minecraft:oak_stairs", map[string]string{"facing": "north"}); err == nil {
		t.Fatal("incomplete properties were accepted")
	}
}

func TestEveryBlockStateRoundTrips(t *testing.T) {
	blocks, err := BlockCount()
	if err != nil || blocks != 1060 {
		t.Fatalf("block count=%d err=%v", blocks, err)
	}
	states, err := BlockStateCount()
	if err != nil || states != 26684 {
		t.Fatalf("state count=%d err=%v", states, err)
	}
	for id := int32(0); id <= 26683; id++ {
		state, err := BlockStateByID(id)
		if err != nil {
			t.Fatalf("resolve state %d: %v", id, err)
		}
		roundTrip, err := BlockStateID(state.Name, state.Properties)
		if err != nil {
			t.Fatalf("encode state %d (%s): %v", id, state.Name, err)
		}
		if roundTrip != id {
			t.Fatalf("state %d (%s) round-tripped as %d", id, state.Name, roundTrip)
		}
	}
}

func TestDefaultBlockStates(t *testing.T) {
	stairs, err := DefaultBlockState("minecraft:oak_stairs")
	if err != nil {
		t.Fatal(err)
	}
	if stairs.ID != 2885 || stairs.Properties["facing"] != "north" || stairs.Properties["half"] != "bottom" || stairs.Properties["shape"] != "straight" || stairs.Properties["waterlogged"] != "false" {
		t.Fatalf("oak stairs default = %+v", stairs)
	}
}
