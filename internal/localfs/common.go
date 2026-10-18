// SPDX-License-Identifier: Apache-2.0
package localfs

import (
	"errors"
	"os"
	"strings"
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

// plainName reports whether name is a single path element inside the root.
func plainName(name string) bool {
	return name != "" && name != "." && name != ".." && !strings.ContainsAny(name, `/\:`)
}
