//go:build windows

package sshterm

import (
	"errors"
	"io"
	"net"
	"os"
	"strings"
)

// agentPipe is the Windows OpenSSH agent's named pipe.
const agentPipe = `\\.\pipe\openssh-ssh-agent`

// dialAgent connects to the agent at path: a named pipe, or a Unix
// socket (which Windows has had since 2018, and Git for Windows' agent
// uses).
func dialAgent(path string) (io.ReadWriteCloser, error) {
	switch {
	case path == "":
		return nil, errors.New("no SSH agent")
	case strings.HasPrefix(path, `\\.\pipe\`):
		return os.OpenFile(path, os.O_RDWR, 0)
	}
	return net.Dial("unix", path)
}

// DefaultAgent is where the person's agent is found when nothing says:
// the socket SSH_AUTH_SOCK names, or else the Windows OpenSSH agent.
func DefaultAgent(getenv func(string) string) string {
	if s := getenv("SSH_AUTH_SOCK"); s != "" {
		return s
	}
	return agentPipe
}
