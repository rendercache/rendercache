// SPDX-License-Identifier: Apache-2.0
package localfs

import (
	"errors"
	"os"
)

var errLocked = errors.New("data directory already locked by another RenderCache process")

func checkRegular(f *os.File) (*os.File, error) {
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

func openLockFile(dir string) (*os.File, error) {
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, err
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	return OpenRegular(root, ".rendercache.lock", os.O_CREATE|os.O_RDWR)
}

// sameAsName checks that f is the file name refers to without following links,
// so a link swapped in between the checks is refused.
func sameAsName(root *os.Root, name string, f *os.File) error {
	opened, err := f.Stat()
	if err != nil {
		return err
	}
	named, err := root.Lstat(name)
	if err != nil {
		return err
	}
	if !named.Mode().IsRegular() || !os.SameFile(opened, named) {
		return errors.New("expected a regular file, not a link")
	}
	return nil
}
