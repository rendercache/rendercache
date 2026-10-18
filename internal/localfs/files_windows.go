// SPDX-License-Identifier: Apache-2.0
//go:build windows

package localfs

import (
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"time"
	"unsafe"
)

var (
	kernel32         = syscall.NewLazyDLL("kernel32.dll")
	procMoveFileExW  = kernel32.NewProc("MoveFileExW")
	procLockFileEx   = kernel32.NewProc("LockFileEx")
	procUnlockFileEx = kernel32.NewProc("UnlockFileEx")
)

const (
	movefileReplaceExisting = 0x1
	movefileWriteThrough    = 0x8
	lockfileFailImmediately = 0x1
	lockfileExclusiveLock   = 0x2

	errorAccessDenied     = syscall.Errno(5)
	errorSharingViolation = syscall.Errno(32)
)

// OpenRegular opens name inside root and accepts only regular files.
// FILE_FLAG_OPEN_REPARSE_POINT opens a symlink or junction itself instead of
// its target, so a link is seen as a non-regular file and refused, the same
// as O_NOFOLLOW on Linux and macOS. Windows has no FIFOs in the filesystem.
func OpenRegular(root *os.Root, name string, flags int) (*os.File, error) {
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
	r, _, _ := procLockFileEx.Call(f.Fd(), lockfileExclusiveLock|lockfileFailImmediately, 0, 1, 0, uintptr(unsafe.Pointer(&overlapped)))
	if r == 0 {
		f.Close()
		return nil, errLocked
	}
	return func() {
		var o syscall.Overlapped
		_, _, _ = procUnlockFileEx.Call(f.Fd(), 0, 1, 0, uintptr(unsafe.Pointer(&o)))
		_ = f.Close()
	}, nil
}

// Rename replaces newname with oldname using MOVEFILE_WRITE_THROUGH, which
// returns only after the move is on disk. Windows cannot sync a directory
// handle, so this is how renames are made durable there. Both names must be
// single path elements inside root.
func Rename(root *os.Root, oldname, newname string) error {
	if !plainName(oldname) || !plainName(newname) {
		return errors.New("rename names must be plain file names")
	}
	if info, err := root.Lstat(newname); err == nil && !info.Mode().IsRegular() {
		return errors.New("refusing to replace a non-regular file")
	}
	from, err := syscall.UTF16PtrFromString(filepath.Join(root.Name(), oldname))
	if err != nil {
		return err
	}
	to, err := syscall.UTF16PtrFromString(filepath.Join(root.Name(), newname))
	if err != nil {
		return err
	}
	// A concurrent reader may hold the target open for a few milliseconds. Files
	// opened through os.Root allow delete sharing, but some Windows versions
	// still refuse to replace an open file, so retry those errors briefly.
	var callErr error
	for attempt := 0; attempt < 50; attempt++ {
		r, _, err := procMoveFileExW.Call(uintptr(unsafe.Pointer(from)), uintptr(unsafe.Pointer(to)), movefileReplaceExisting|movefileWriteThrough)
		if r != 0 {
			return nil
		}
		callErr = err
		if err != errorAccessDenied && err != errorSharingViolation {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	return &os.LinkError{Op: "rename", Old: oldname, New: newname, Err: callErr}
}

// SyncDir is a no-op on Windows: directory handles cannot be flushed, and
// renames already use MOVEFILE_WRITE_THROUGH. Removals rely on NTFS journaling.
func SyncDir(*os.Root) error { return nil }
