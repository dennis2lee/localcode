//go:build windows

package browser

import (
	"fmt"
	"os/exec"
	"strings"
	"syscall"
	"unsafe"

	"localcode/internal/childproc"
)

// Diagnostic branch only: which processes have a file open, by the
// Restart Manager, and the processes there are.

var (
	rstrtmgr                = syscall.NewLazyDLL("rstrtmgr.dll")
	procRmStartSession      = rstrtmgr.NewProc("RmStartSession")
	procRmRegisterResources = rstrtmgr.NewProc("RmRegisterResources")
	procRmGetList           = rstrtmgr.NewProc("RmGetList")
	procRmEndSession        = rstrtmgr.NewProc("RmEndSession")
)

type rmUniqueProcess struct {
	ProcessID uint32
	StartTime syscall.Filetime
}

type rmProcessInfo struct {
	Process          rmUniqueProcess
	AppName          [256]uint16
	ServiceShortName [64]uint16
	ApplicationType  uint32
	AppStatus        uint32
	TSSessionID      uint32
	Restartable      int32
}

// holders names the processes that have path open.
func holders(path string) string {
	var session uint32
	var key [33]uint16
	if r, _, _ := procRmStartSession.Call(uintptr(unsafe.Pointer(&session)), 0, uintptr(unsafe.Pointer(&key[0]))); r != 0 {
		return fmt.Sprintf("RmStartSession=%d", r)
	}
	defer procRmEndSession.Call(uintptr(session))
	name, err := syscall.UTF16PtrFromString(path)
	if err != nil {
		return err.Error()
	}
	names := []*uint16{name}
	if r, _, _ := procRmRegisterResources.Call(uintptr(session), 1, uintptr(unsafe.Pointer(&names[0])), 0, 0, 0, 0); r != 0 {
		return fmt.Sprintf("RmRegisterResources=%d", r)
	}
	var needed, reasons uint32
	infos := make([]rmProcessInfo, 16)
	count := uint32(len(infos))
	r, _, _ := procRmGetList.Call(uintptr(session), uintptr(unsafe.Pointer(&needed)), uintptr(unsafe.Pointer(&count)), uintptr(unsafe.Pointer(&infos[0])), uintptr(unsafe.Pointer(&reasons)))
	if r != 0 {
		return fmt.Sprintf("RmGetList=%d needed=%d", r, needed)
	}
	if count == 0 {
		return "nobody"
	}
	var out []string
	for _, in := range infos[:count] {
		out = append(out, fmt.Sprintf("%d(%s)", in.Process.ProcessID, syscall.UTF16ToString(in.AppName[:])))
	}
	return strings.Join(out, ",")
}

// processTable lists every process: its id, its parent's and its program.
func processTable() string {
	snap, err := syscall.CreateToolhelp32Snapshot(syscall.TH32CS_SNAPPROCESS, 0)
	if err != nil {
		return err.Error()
	}
	defer syscall.CloseHandle(snap)
	var e syscall.ProcessEntry32
	e.Size = uint32(unsafe.Sizeof(e))
	var out []string
	for err = syscall.Process32First(snap, &e); err == nil; err = syscall.Process32Next(snap, &e) {
		name := syscall.UTF16ToString(e.ExeFile[:])
		if strings.EqualFold(name, "chrome.exe") || strings.EqualFold(name, "msedge.exe") {
			out = append(out, fmt.Sprintf("%d<-%d", e.ProcessID, e.ParentProcessID))
		}
	}
	return strings.Join(out, " ")
}

// commandLines lists the browser processes with what each was started
// with. Slow: it starts a PowerShell.
func commandLines() string {
	cmd := exec.Command("powershell", "-NoProfile", "-NonInteractive", "-Command",
		`Get-CimInstance Win32_Process -Filter "Name='chrome.exe'" | ForEach-Object { $c = [string]$_.CommandLine; $t = ''; if ($c -match '--type=(\S+)') { $t = $Matches[1] }; $s = ''; if ($c -match '--utility-sub-type=(\S+)') { $s = $Matches[1] }; $d = ''; if ($c -match '--user-data-dir=("[^"]*"|\S+)') { $d = $Matches[1] }; "$($_.ProcessId)<-$($_.ParentProcessId) type=$t $s dir=$d" }`)
	childproc.Hide(cmd)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Sprintf("%v: %s", err, out)
	}
	return strings.ReplaceAll(strings.TrimSpace(string(out)), "\r\n", " | ")
}
