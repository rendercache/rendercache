// SPDX-License-Identifier: Apache-2.0
//go:build windows

package localfs

import "testing"

func TestWindowsRenameRefusesPathNames(t *testing.T) {
	root, _ := openRoot(t)
	for _, pair := range [][2]string{{`..\x`, "y"}, {"x", `sub\y`}, {"x", "C:y"}} {
		if err := Rename(root, pair[0], pair[1]); err == nil {
			t.Fatalf("accepted %q -> %q", pair[0], pair[1])
		}
	}
}
