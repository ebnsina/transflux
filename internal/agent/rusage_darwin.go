//go:build darwin

package agent

import "syscall"

// macOS reports maximum resident set size in bytes, unlike Linux.
func maxRSSBytes(u *syscall.Rusage) int64 { return u.Maxrss }
