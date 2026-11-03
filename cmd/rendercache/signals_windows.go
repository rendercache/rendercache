// SPDX-License-Identifier: Apache-2.0
//go:build windows

package main

import "os"

// Windows has no SIGHUP; reload with POST /reload on the admin listener.
var reloadSignals []os.Signal
