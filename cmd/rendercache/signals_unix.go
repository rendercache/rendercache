// SPDX-License-Identifier: Apache-2.0
//go:build !windows

package main

import (
	"os"
	"syscall"
)

// SIGHUP reloads the configuration and bot lists during serve.
var reloadSignals = []os.Signal{syscall.SIGHUP}
