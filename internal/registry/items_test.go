// SPDX-License-Identifier: AGPL-3.0-only

package registry

import "testing"

func TestItemRegistry1211(t *testing.T) {
	stone, ok, err := ItemByName("minecraft:stone")
	if err != nil || !ok || stone.ID != 1 || stone.StackSize != 64 {
		t.Fatalf("stone=%#v ok=%t err=%v", stone, ok, err)
	}
	byID, ok, err := ItemByID(1)
	if err != nil || !ok || byID.Name != "stone" {
		t.Fatalf("item 1=%#v ok=%t err=%v", byID, ok, err)
	}
}
