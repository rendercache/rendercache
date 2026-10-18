// SPDX-License-Identifier: Apache-2.0
//go:build linux || darwin

// Package localfs confines application files to an opened directory and refuses
// symlinks/special files. The initial configured directory is administrator-owned.
package localfs

import (
	"errors"
	"os"
	"syscall"
)

// OpenRegular opens name inside root without following a symlink and without
// blocking on a FIFO, and accepts only regular files. os.Root itself follows
// links that stay inside the root, so the name is also checked with Lstat and
// the opened file must be that same regular file.
func OpenRegular(root *os.Root, name string, flags int) (*os.File, error) {
	if info, err := root.Lstat(name); err == nil && !info.Mode().IsRegular() {
		return nil, errors.New("expected a regular file")
	}
	f, err := root.OpenFile(name, flags|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0600)
	if err != nil {
		return nil, err
	}
	if f, err = checkRegular(f); err != nil {
		return nil, err
	}
	if err = sameAsName(root, name, f); err != nil {
		f.Close()
		return nil, err
	}
	return f, nil
}

// Lock is advisory between RenderCache commands. Keep its inode in place across
// unlocks; unlinking a live lock would permit two independent lock owners.
func Lock(dir string) (func(), error) {
	f, err := openLockFile(dir)
	if err != nil {
		return nil, err
	}
	if err = syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		return nil, errLocked
	}
	return func() { _ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN); _ = f.Close() }, nil
}

// Rename atomically replaces newname with oldname inside root. Call SyncDir
// afterwards to make the change durable.
func Rename(root *os.Root, oldname, newname string) error {
	return root.Rename(oldname, newname)
}

// SyncDir flushes directory entries (creations, renames, removals) to disk.
// On macOS, os.File.Sync uses F_FULLFSYNC.
func SyncDir(root *os.Root) error {
	dir, err := root.Open(".")
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}
