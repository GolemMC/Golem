// SPDX-License-Identifier: AGPL-3.0-only

package protocol

import "fmt"

type State int32

const (
	StateHandshake State = iota
	StateStatus
	StateLogin
	StateConfiguration
	StatePlay
)

func (s State) String() string {
	switch s {
	case StateHandshake:
		return "handshake"
	case StateStatus:
		return "status"
	case StateLogin:
		return "login"
	case StateConfiguration:
		return "configuration"
	case StatePlay:
		return "play"
	default:
		return fmt.Sprintf("unknown(%d)", s)
	}
}

func NextFromHandshake(v int32) (State, error) {
	s := State(v)
	if s != StateStatus && s != StateLogin {
		return 0, fmt.Errorf("unsupported next protocol state %d", v)
	}
	return s, nil
}
