//go:build unix

package php

import "syscall"

// reloadSignal asks an FPM master to re-read its configuration.
//
// SIGUSR2 is FPM's graceful reload: workers are restarted but the listening
// socket is kept, so requests in flight are not dropped. SIGHUP would also
// reload, but it re-opens logs as well and is the signal an init system sends,
// which makes it ambiguous when both are involved.
const reloadSignal = syscall.SIGUSR2
