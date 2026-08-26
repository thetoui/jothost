//go:build !unix

package php

import "os"

// reloadSignal is a placeholder off Unix.
//
// The Agent only runs on Linux; this keeps the module building on a
// developer's machine (ARCHITECTURE.md section 16), where signalling a
// process this way is not meaningful and Signal returns an error.
var reloadSignal os.Signal = os.Interrupt
