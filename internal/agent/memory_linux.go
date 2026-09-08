//go:build linux

package agent

import (
	"os"
	"strings"
)

func totalMemory() int64 {
	data, err := os.ReadFile("/proc/meminfo")
	if err != nil {
		return 0
	}
	for _, line := range strings.Split(string(data), "\n") {
		if key, value, ok := strings.Cut(line, ":"); ok && key == "MemTotal" {
			// MemTotal is reported in kB.
			return atoi(strings.TrimSuffix(strings.TrimSpace(value), " kB")) * 1024
		}
	}
	return 0
}
