//go:build linux

package agent

import "syscall"

// Linux reports maximum resident set size in kilobytes.
func maxRSSBytes(u *syscall.Rusage) int64 { return u.Maxrss * 1024 }
