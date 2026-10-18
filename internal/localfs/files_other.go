// SPDX-License-Identifier: Apache-2.0
//go:build !linux && !darwin && !windows

package localfs

import (
	"errors"
	"os"
)

var errUnsupported = errors.New("RenderCache supports Linux, macOS and Windows")

func OpenRegular(*os.Root, string, int) (*os.File, error) { return nil, errUnsupported }
func Lock(string) (func(), error)                         { return nil, errUnsupported }
func Rename(*os.Root, string, string) error               { return errUnsupported }
func SyncDir(*os.Root) error                              { return errUnsupported }
