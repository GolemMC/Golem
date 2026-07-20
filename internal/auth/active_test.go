// SPDX-License-Identifier: AGPL-3.0-only

package auth

import "testing"

func TestActiveLoginsRejectDuplicatesAndCapacity(t *testing.T) {
	active := NewActiveLogins(1)
	first := [16]byte{1}
	if err := active.Reserve(first, "one"); err != nil {
		t.Fatal(err)
	}
	if err := active.Reserve(first, "one"); err == nil {
		t.Fatal("duplicate account accepted")
	}
	if err := active.Reserve([16]byte{2}, "two"); err == nil {
		t.Fatal("capacity limit not enforced")
	}
	active.Release(first)
	if err := active.Reserve([16]byte{2}, "two"); err != nil {
		t.Fatal(err)
	}
}
