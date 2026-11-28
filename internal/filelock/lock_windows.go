//go:build windows

package filelock

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
	lockfileExclusiveLock   = 0x2
	lockfileFailImmediately = 0x1
)

func tryLock(file *os.File) error {
	var ol syscall.Overlapped
	r1, _, err := procLockFileEx.Call(
		uintptr(file.Fd()),
		uintptr(lockfileExclusiveLock|lockfileFailImmediately),
		0,
		1,
		0,
		uintptr(unsafe.Pointer(&ol)),
	)
	if r1 == 0 {
		return err
	}
	return nil
}

func unlock(file *os.File) {
	var ol syscall.Overlapped
	procUnlockFileEx.Call(
		uintptr(file.Fd()),
		0,
		1,
		0,
		uintptr(unsafe.Pointer(&ol)),
	)
}
