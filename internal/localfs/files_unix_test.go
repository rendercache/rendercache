// SPDX-License-Identifier: Apache-2.0
//go:build linux || darwin

package localfs

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

func TestRejectPipesWithoutBlocking(t *testing.T) {
	root, dir := openRoot(t)
	if err := syscall.Mkfifo(filepath.Join(dir, "pipe"), 0600); err != nil {
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
