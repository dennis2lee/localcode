//go:build !windows

package sshterm

import (
	"errors"
	"io"
	"net"
)

// dialAgent connects to the agent socket at path.
func dialAgent(path string) (io.ReadWriteCloser, error) {
	if path == "" {
		return nil, errors.New("no SSH agent")
	}
	return net.Dial("unix", path)
}

// DefaultAgent is where the person's agent is found when nothing says:
// the socket SSH_AUTH_SOCK names.
func DefaultAgent(getenv func(string) string) string {
	return getenv("SSH_AUTH_SOCK")
}
