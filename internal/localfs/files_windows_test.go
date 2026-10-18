// SPDX-License-Identifier: Apache-2.0
//go:build windows

package localfs

import (
	"os"
	"path/filepath"
	"testing"
)

// A crawler may be reading a snapshot while a refresh replaces it.
func TestWindowsRenameReplacesOpenFile(t *testing.T) {
	root, dir := openRoot(t)
	os.WriteFile(filepath.Join(dir, "record.json"), []byte("old"), 0600)
	os.WriteFile(filepath.Join(dir, "new.tmp"), []byte("new"), 0600)
	reader, err := OpenRegular(root, "record.json", os.O_RDONLY)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	if err := Rename(root, "new.tmp", "record.json"); err != nil {
		t.Fatal("rename over an open file:", err)
	}
	b, err := os.ReadFile(filepath.Join(dir, "record.json"))
	if err != nil || string(b) != "new" {
		t.Fatal("rename did not replace the record", string(b), err)
	}
}
