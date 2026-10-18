// SPDX-License-Identifier: Apache-2.0
package localfs

import (
	"os"
	"path/filepath"
	"testing"
)

func openRoot(t *testing.T) (*os.Root, string) {
	t.Helper()
	dir := t.TempDir()
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { root.Close() })
	return root, dir
}

func TestRejectSymlinksInsideAndOutsideRoot(t *testing.T) {
	root, dir := openRoot(t)
	if err := os.WriteFile(filepath.Join(dir, "real"), []byte("public"), 0600); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(t.TempDir(), "private")
	os.WriteFile(outside, []byte("secret"), 0600)
	if err := os.Symlink(filepath.Join(dir, "real"), filepath.Join(dir, "inner")); err != nil {
		t.Skip("symlinks unavailable here:", err)
	}
	os.Symlink(outside, filepath.Join(dir, "outer"))
	for _, name := range []string{"inner", "outer"} {
		if f, err := OpenRegular(root, name, os.O_RDONLY); err == nil {
			f.Close()
			t.Fatalf("symlink %s opened", name)
		}
	}
	f, err := OpenRegular(root, "real", os.O_RDONLY)
	if err != nil {
		t.Fatal("regular file refused:", err)
	}
	f.Close()
}

func TestRejectDirectories(t *testing.T) {
	root, dir := openRoot(t)
	os.Mkdir(filepath.Join(dir, "sub"), 0700)
	if f, err := OpenRegular(root, "sub", os.O_RDONLY); err == nil {
		f.Close()
		t.Fatal("directory opened as a regular file")
	}
}

func TestRenameReplacesAndSyncs(t *testing.T) {
	root, dir := openRoot(t)
	os.WriteFile(filepath.Join(dir, "new.tmp"), []byte("new"), 0600)
	os.WriteFile(filepath.Join(dir, "record.json"), []byte("old"), 0600)
	if err := Rename(root, "new.tmp", "record.json"); err != nil {
		t.Fatal(err)
	}
	if err := SyncDir(root); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(dir, "record.json"))
	if err != nil || string(b) != "new" {
		t.Fatal("rename did not replace the record", string(b), err)
	}
	if _, err := os.Stat(filepath.Join(dir, "new.tmp")); !os.IsNotExist(err) {
		t.Fatal("temporary name still present")
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
