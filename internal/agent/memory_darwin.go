//go:build darwin

package agent

import "os/exec"

func totalMemory() int64 {
	out, err := exec.Command("sysctl", "-n", "hw.memsize").Output()
	if err != nil {
		return 0
	}
	return atoi(string(out))
}
