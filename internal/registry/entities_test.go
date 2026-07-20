// SPDX-License-Identifier: AGPL-3.0-only

package registry

import "testing"

func TestEveryEntityDefinitionIsAddressable(t *testing.T) {
	count, err := EntityCount()
	if err != nil {
		t.Fatal(err)
	}
	if count != 130 {
		t.Fatalf("entity count=%d", count)
	}
	for id := int32(0); id < int32(count); id++ {
		entity, ok, err := EntityByID(id)
		if err != nil || !ok {
			t.Fatalf("entity %d: ok=%v err=%v", id, ok, err)
		}
		byName, ok, err := EntityByName("minecraft:" + entity.Name)
		if err != nil || !ok || byName.ID != id {
			t.Fatalf("entity %d/%s reverse lookup: %+v ok=%v err=%v", id, entity.Name, byName, ok, err)
		}
	}
	player, ok, err := EntityByName("player")
	if err != nil || !ok || player.ID != PlayerEntityTypeID || player.Width != 0.6 || player.Height != 1.8 {
		t.Fatalf("player entity=%+v ok=%v err=%v", player, ok, err)
	}
}

func TestEntityMetadataKeysAreCopied(t *testing.T) {
	first, _, err := EntityByName("allay")
	if err != nil {
		t.Fatal(err)
	}
	first.MetadataKeys[0] = "mutated"
	second, _, err := EntityByName("allay")
	if err != nil {
		t.Fatal(err)
	}
	if second.MetadataKeys[0] != "shared_flags" {
		t.Fatal("caller mutated the entity registry")
	}
}
