//go:build windows

package main

import (
	"os/exec"
	"strconv"
	"strings"
)

func processExists(pid int) bool {
	out, err := exec.Command("tasklist", "/FI", "PID eq "+strconv.Itoa(pid), "/NH", "/FO", "CSV").Output()
	if err != nil {
		return false
	}
	// tasklist prints a quoted CSV row for a match and an "INFO: No tasks"
	// line for none.
	return strings.Contains(string(out), "\""+strconv.Itoa(pid)+"\"")
}
