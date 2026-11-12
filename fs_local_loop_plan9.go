//go:build plan9

package webdav

// errLoop is nil, matching no error: Plan 9 has no symbolic links, and its
// syscall package no ELOOP.
var errLoop error
