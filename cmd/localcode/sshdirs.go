package main

import (
	"os"
	"os/user"
	"path/filepath"
	"runtime"
)

// sshSystemDir is the system's SSH directory, for ssh_config and
// ssh_known_hosts, as OpenSSH finds it.
func sshSystemDir() string {
	if runtime.GOOS == "windows" {
		if pd := os.Getenv("ProgramData"); pd != "" {
			return filepath.Join(pd, "ssh")
		}
		return ""
	}
	return "/etc/ssh"
}

// localUserName is the user name an SSH connection logs in as when
// nothing names another, as OpenSSH does.
func localUserName() string {
	if u, err := user.Current(); err == nil && u.Username != "" {
		name := u.Username
		// Windows names a user DOMAIN\user.
		if i := lastSep(name); i >= 0 {
			name = name[i+1:]
		}
		return name
	}
	if n := os.Getenv("USER"); n != "" {
		return n
	}
	return os.Getenv("USERNAME")
}

func lastSep(s string) int {
	for i := len(s) - 1; i >= 0; i-- {
		if s[i] == '\\' {
			return i
		}
	}
	return -1
}
