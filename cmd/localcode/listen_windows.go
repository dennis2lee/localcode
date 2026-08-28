//go:build windows

package main

import (
	"errors"
	"syscall"
)

// wsaeAddrInUse is WSAEADDRINUSE, which is what a failed bind actually
// returns on Windows.
//
// syscall.EADDRINUSE exists on Windows too, but it is a placeholder
// built on APPLICATION_ERROR rather than the Winsock number, so nothing
// net returns ever matches it. Checking only that constant would compile
// here, look right, and leave Windows with the hard failure this file
// exists to remove.
//
// Written as the number because the standard syscall package does not
// export it for Windows; golang.org/x/sys/windows declares the same
// 10048, and one constant is not worth the dependency. Both are checked,
// since a wrapped EADDRINUSE from anywhere else still means the same
// thing.
const wsaeAddrInUse = syscall.Errno(10048)

func addrInUse(err error) bool {
	return errors.Is(err, syscall.EADDRINUSE) || errors.Is(err, wsaeAddrInUse)
}
