// SPDX-License-Identifier: Apache-2.0
//go:build !linux

package localfs

import (
	"errors"
	"os"
)

func OpenRegular(*os.Root, string, int) (*os.File, error) {
	return nil, errors.New("secure file operations currently support Linux only")
}
func Lock(string) (func(), error) {
	return nil, errors.New("RenderCache currently supports Linux only")
}
