// SPDX-License-Identifier: AGPL-3.0-only
//go:build windows

package world

import (
	"os"
	"syscall"
	"unsafe"
)

var (
	modkernel32      = syscall.NewLazyDLL("kernel32.dll")
	procLockFileEx   = modkernel32.NewProc("LockFileEx")
	procUnlockFileEx = modkernel32.NewProc("UnlockFileEx")
)

const (
	LOCKFILE_FAIL_IMMEDIATELY = 0x00000001
	LOCKFILE_EXCLUSIVE_LOCK   = 0x00000002
)

func lockFile(f *os.File) error {
	var overlapped syscall.Overlapped
	ret, _, err := procLockFileEx.Call(
		f.Fd(),
		uintptr(LOCKFILE_EXCLUSIVE_LOCK|LOCKFILE_FAIL_IMMEDIATELY),
		0,
		0xFFFFFFFF,
		0xFFFFFFFF,
		uintptr(unsafe.Pointer(&overlapped)),
	)
	if ret == 0 {
		return err
	}
	return nil
}

func unlockFile(f *os.File) error {
	var overlapped syscall.Overlapped
	ret, _, err := procUnlockFileEx.Call(
		f.Fd(),
		0,
		0xFFFFFFFF,
		0xFFFFFFFF,
		uintptr(unsafe.Pointer(&overlapped)),
	)
	if ret == 0 {
		return err
	}
	return nil
}
