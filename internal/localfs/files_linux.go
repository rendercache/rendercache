// SPDX-License-Identifier: Apache-2.0
//go:build linux

// Package localfs confines application files to an opened directory and refuses
// symlinks/special files. The initial configured directory is administrator-owned.
package localfs

import (
	"errors"
	"os"
	"syscall"
)

func OpenRegular(root *os.Root, name string, flags int) (*os.File, error) {
	f, err := root.OpenFile(name, flags|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0600)
	if err != nil {
		return nil, err
	}
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() {
		f.Close()
		if err != nil {
			return nil, err
		}
		return nil, errors.New("expected a regular file")
	}
	return f, nil
}

// Lock is advisory between RenderCache commands. Keep its inode in place across
// unlocks; unlinking a live lock would permit two independent lock owners.
func Lock(dir string) (func(), error) {
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, err
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	f, err := OpenRegular(root, ".rendercache.lock", os.O_CREATE|os.O_RDWR)
	if err != nil {
		return nil, err
	}
	if err = syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		return nil, errors.New("data directory already locked by another RenderCache process")
	}
	return func() { _ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN); _ = f.Close() }, nil
}
