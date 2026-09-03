//go:build !windows

package main

import (
	"os/exec"
	"strconv"
	"strings"
)

func processExists(pid int) bool {
	out, err := exec.Command("ps", "-p", strconv.Itoa(pid), "-o", "stat=").Output()
	if err != nil {
		return false
	}
	// A zombie is not a running process; it is one whose exit nobody has
	// read yet, and spawnSuccessor's Wait goroutine reads it.
	stat := strings.TrimSpace(string(out))
	return stat != "" && !strings.HasPrefix(stat, "Z")
}
