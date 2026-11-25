//go:build windows

package browsertest

import (
	"fmt"
	"unsafe"

	"golang.org/x/sys/windows"
)

// The Restart Manager is the part of Windows an installer asks which
// programs have a file open. It answers for any process of this user.
var (
	rstrtmgr                = windows.NewLazySystemDLL("rstrtmgr.dll")
	procRmStartSession      = rstrtmgr.NewProc("RmStartSession")
	procRmRegisterResources = rstrtmgr.NewProc("RmRegisterResources")
	procRmGetList           = rstrtmgr.NewProc("RmGetList")
	procRmEndSession        = rstrtmgr.NewProc("RmEndSession")
)

// rmProcessInfo is RM_PROCESS_INFO: 668 bytes, every field aligned to 4.
type rmProcessInfo struct {
	pid             uint32
	started         windows.Filetime
	appName         [256]uint16 // CCH_RM_MAX_APP_NAME + 1
	serviceName     [64]uint16  // CCH_RM_MAX_SVC_NAME + 1
	applicationType uint32
	appStatus       uint32
	tsSessionID     uint32
	restartable     int32
}

// holders names the processes holding one of paths open: each one's
// process id, the name Windows has for the program, and its command line,
// which for a browser says which of its processes it is and which profile
// it runs on.
func holders(paths []string) []string {
	if len(paths) == 0 {
		return nil
	}
	if err := rstrtmgr.Load(); err != nil {
		return []string{"unknown (" + err.Error() + ")"}
	}
	unknown := func(call string, rc uintptr) []string {
		return []string{fmt.Sprintf("unknown (%s: error %d)", call, rc)}
	}
	var session uint32
	var key [33]uint16 // CCH_RM_SESSION_KEY + 1
	if rc, _, _ := procRmStartSession.Call(uintptr(unsafe.Pointer(&session)), 0, uintptr(unsafe.Pointer(&key[0]))); rc != 0 {
		return unknown("RmStartSession", rc)
	}
	defer procRmEndSession.Call(uintptr(session))

	names := make([]*uint16, 0, len(paths))
	for _, p := range paths {
		if u, err := windows.UTF16PtrFromString(longPath(p)); err == nil {
			names = append(names, u)
		}
	}
	if len(names) == 0 {
		return nil
	}
	if rc, _, _ := procRmRegisterResources.Call(uintptr(session), uintptr(len(names)), uintptr(unsafe.Pointer(&names[0])), 0, 0, 0, 0); rc != 0 {
		return unknown("RmRegisterResources", rc)
	}

	infos := make([]rmProcessInfo, 16)
	var needed, got, reasons uint32
	for try := 0; ; try++ {
		got = uint32(len(infos))
		rc, _, _ := procRmGetList.Call(uintptr(session), uintptr(unsafe.Pointer(&needed)), uintptr(unsafe.Pointer(&got)),
			uintptr(unsafe.Pointer(&infos[0])), uintptr(unsafe.Pointer(&reasons)))
		if rc == 0 {
			break
		}
		// More holders than there was room for: asked again with room,
		// and a few over for one that turns up in between.
		if rc != uintptr(windows.ERROR_MORE_DATA) || try == 3 {
			return unknown("RmGetList", rc)
		}
		infos = make([]rmProcessInfo, needed+8)
	}
	var out []string
	for _, in := range infos[:got] {
		who := fmt.Sprintf("process %d (%s)", in.pid, windows.UTF16ToString(in.appName[:]))
		if cmd := commandLine(in.pid); cmd != "" {
			who += ": " + cmd
		}
		out = append(out, who)
	}
	return out
}

// longPath is path without the short names in it (RUNNER~1), which is how
// the temporary directory is spelled on a CI runner and not how the
// system names an open file.
func longPath(path string) string {
	p, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return path
	}
	buf := make([]uint16, 1024)
	n, err := windows.GetLongPathName(p, &buf[0], uint32(len(buf)))
	if err != nil || n == 0 || int(n) > len(buf) {
		return path
	}
	return windows.UTF16ToString(buf[:n])
}

// maxCommandLine is how much of a command line is shown: a browser's own
// runs to thousands of characters, and which process it is, is said in
// the first few hundred.
const maxCommandLine = 600

// commandLine is the command line process pid was started with, or
// nothing when the process has gone or is not this user's to ask about.
func commandLine(pid uint32) string {
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, pid)
	if err != nil {
		return ""
	}
	defer windows.CloseHandle(h)
	// A UNICODE_STRING and, after it, the text it points at. uint64s so
	// the pointer in it is aligned.
	buf := make([]uint64, 8<<10)
	var n uint32
	if err := windows.NtQueryInformationProcess(h, windows.ProcessCommandLineInformation, unsafe.Pointer(&buf[0]), uint32(len(buf)*8), &n); err != nil {
		return ""
	}
	cmd := []rune((*windows.NTUnicodeString)(unsafe.Pointer(&buf[0])).String())
	if len(cmd) > maxCommandLine {
		return string(cmd[:maxCommandLine]) + "..."
	}
	return string(cmd)
}
