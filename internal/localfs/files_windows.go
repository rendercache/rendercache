// SPDX-License-Identifier: Apache-2.0
//go:build windows

package localfs

import (
	"errors"
	"os"
	"syscall"
	"unsafe"
)

var (
	kernel32         = syscall.NewLazyDLL("kernel32.dll")
	procLockFileEx   = kernel32.NewProc("LockFileEx")
	procUnlockFileEx = kernel32.NewProc("UnlockFileEx")
)

const (
	lockfileFailImmediately = 0x1
	lockfileExclusiveLock   = 0x2

	errorLockViolation = syscall.Errno(33)
)

// OpenRegular opens name inside root and accepts only regular files.
// FILE_FLAG_OPEN_REPARSE_POINT opens a symlink or junction itself instead of
// its target, so a link is seen as a non-regular file and refused, the same
// as O_NOFOLLOW on Linux and macOS. Windows has no FIFOs in the filesystem.
func OpenRegular(root *os.Root, name string, flags int) (*os.File, error) {
	if info, err := root.Lstat(name); err == nil && !info.Mode().IsRegular() {
		return nil, errors.New("expected a regular file")
	}
	f, err := root.OpenFile(name, flags|syscall.FILE_FLAG_OPEN_REPARSE_POINT, 0600)
	if err != nil {
		return nil, err
	}
	return checkRegular(f)
}

// Lock holds an exclusive LockFileEx lock on the data directory's lock file.
func Lock(dir string) (func(), error) {
	f, err := openLockFile(dir)
	if err != nil {
		return nil, err
	}
	var overlapped syscall.Overlapped
	r, _, callErr := procLockFileEx.Call(f.Fd(), lockfileExclusiveLock|lockfileFailImmediately, 0, 1, 0, uintptr(unsafe.Pointer(&overlapped)))
	if r == 0 {
		f.Close()
		if callErr == errorLockViolation {
			return nil, errLocked
		}
		return nil, callErr
	}
	return func() {
		var o syscall.Overlapped
		_, _, _ = procUnlockFileEx.Call(f.Fd(), 0, 1, 0, uintptr(unsafe.Pointer(&o)))
		_ = f.Close()
	}, nil
}

// Rename atomically replaces newname with oldname inside root. On Windows,
// os.Root renames relative to the directory handle with POSIX semantics, so the
// target may be replaced while a reader still has it open.
func Rename(root *os.Root, oldname, newname string) error {
	return root.Rename(oldname, newname)
}

// SyncDir is a no-op on Windows, where directory handles cannot be flushed.
// NTFS journals directory changes, so a crash leaves either the old or the new
// snapshot in place, though the most recent rename may be lost on power failure.
func SyncDir(*os.Root) error { return nil }
