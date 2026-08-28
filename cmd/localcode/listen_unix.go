//go:build !windows

package main

import (
	"errors"
	"syscall"
)

// addrInUse reports whether err is the one bind failure worth recovering
// from. A permission error or an unresolvable host is a different
// problem and keeps its own message.
func addrInUse(err error) bool {
	return errors.Is(err, syscall.EADDRINUSE)
}
