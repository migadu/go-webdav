//go:build !plan9

package webdav

import "syscall"

// errLoop is the error os.Root fails with for a path which resolves through
// symbolic links which loop, or more of them than it follows. On Windows too,
// as os.Root follows them itself.
var errLoop error = syscall.ELOOP
