//go:build linux

// SPDX-License-Identifier: Apache-2.0
package localfs

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

func TestRejectSymlinksAndPipesWithoutBlocking(t *testing.T) {
	dir := t.TempDir()
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	outside := filepath.Join(t.TempDir(), "private")
	os.WriteFile(outside, []byte("secret"), 0600)
	os.Symlink(outside, filepath.Join(dir, "link"))
	if f, err := OpenRegular(root, "link", os.O_RDONLY); err == nil {
		f.Close()
		t.Fatal("symlink opened")
	}
	if err = syscall.Mkfifo(filepath.Join(dir, "pipe"), 0600); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		f, err := OpenRegular(root, "pipe", os.O_RDONLY)
		if f != nil {
			f.Close()
		}
		done <- err
	}()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("pipe accepted")
		}
	case <-time.After(time.Second):
		t.Fatal("pipe blocked")
	}
}
func TestExclusiveProcessLock(t *testing.T) {
	dir := t.TempDir()
	unlock, err := Lock(dir)
	if err != nil {
		t.Fatal(err)
	}
	if second, err := Lock(dir); err == nil {
		second()
		t.Fatal("double ownership allowed")
	}
	unlock()
	unlock, err = Lock(dir)
	if err != nil {
		t.Fatal(err)
	}
	unlock()
}
