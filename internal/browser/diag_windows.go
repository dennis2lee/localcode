//go:build windows

package browser

import (
	"fmt"
	"unsafe"

	"golang.org/x/sys/windows"
)

// diagChildren is the processes whose parent is pid: each one's id and
// which of the browser's processes it is.
func diagChildren(pid int) []string {
	if pid <= 0 {
		return nil
	}
	snap, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPPROCESS, 0)
	if err != nil {
		return []string{"snapshot: " + err.Error()}
	}
	defer windows.CloseHandle(snap)
	var pe windows.ProcessEntry32
	pe.Size = uint32(unsafe.Sizeof(pe))
	var out []string
	for err = windows.Process32First(snap, &pe); err == nil; err = windows.Process32Next(snap, &pe) {
		if int(pe.ParentProcessID) != pid {
			continue
		}
		cmd, alive := diagCommandLine(pe.ProcessID)
		if !alive {
			continue // listed, and already gone
		}
		out = append(out, fmt.Sprintf("%d:%s:%s", pe.ProcessID, windows.UTF16ToString(pe.ExeFile[:]), diagKind(cmd)))
	}
	return out
}

// diagCommandLine is a process's command line, and whether the process is
// still running.
func diagCommandLine(pid uint32) (string, bool) {
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION|windows.SYNCHRONIZE, false, pid)
	if err != nil {
		return "", false
	}
	defer windows.CloseHandle(h)
	if ev, _ := windows.WaitForSingleObject(h, 0); ev == windows.WAIT_OBJECT_0 {
		return "", false
	}
	buf := make([]uint64, 8<<10)
	var n uint32
	if err := windows.NtQueryInformationProcess(h, windows.ProcessCommandLineInformation, unsafe.Pointer(&buf[0]), uint32(len(buf)*8), &n); err != nil {
		return "", true
	}
	return (*windows.NTUnicodeString)(unsafe.Pointer(&buf[0])).String(), true
}
