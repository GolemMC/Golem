// SPDX-License-Identifier: AGPL-3.0-only

package auth

import (
	"errors"
	"sync"
)

// ActiveLogins provides duplicate-account and player-capacity protection for
// authenticated identities. It deliberately contains no connection behavior.
type ActiveLogins struct {
	mu      sync.Mutex
	maximum int
	active  map[[16]byte]string
}

func NewActiveLogins(maximum int) *ActiveLogins {
	return &ActiveLogins{maximum: maximum, active: make(map[[16]byte]string)}
}

func (a *ActiveLogins) Reserve(id [16]byte, username string) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if _, exists := a.active[id]; exists {
		return errors.New("That account is already connected")
	}
	if len(a.active) >= a.maximum {
		return errors.New("The server is full")
	}
	a.active[id] = username
	return nil
}

func (a *ActiveLogins) Release(id [16]byte) {
	a.mu.Lock()
	delete(a.active, id)
	a.mu.Unlock()
}
