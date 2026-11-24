//go:build windows

// conpty starts a shell under a Windows pseudo console, types a few lines
// into it, and prints exactly what came back, for the PowerShell panel's
// design: whether the system's pseudo console passes the shell's OSC 133
// marks through, what it does to the stream otherwise, and how a resize
// and Korean text come back.
//
//	conpty -shell powershell.exe|pwsh.exe [-profile] [-cols 100 -rows 30]
package main

import (
	"encoding/base64"
	"flag"
	"fmt"
	"os"
	"regexp"
	"strings"
	"sync"
	"time"
	"unicode/utf16"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	shell   = flag.String("shell", "powershell.exe", "the shell to start")
	profile = flag.Bool("profile", false, "load the shell's profile")
	cols    = flag.Int("cols", 100, "columns")
	rows    = flag.Int("rows", 30, "rows")
)

const nonce = "a1b2c3d4e5f6"

// The integration, as the panel would give it: a prompt that prints the D
// mark with the last command's status before the person's own prompt, and
// a C mark when the line is read, through PSReadLine's PSConsoleHostReadLine
// where there is one. A title change carries the same marks, to see whether
// that way through survives where OSC 133 does not.
const integration = `
$global:__lc_n='` + nonce + `'
$global:__lc_e=[char]27
$global:__lc_b=[char]7
$global:__lc_prompt=$function:prompt
$global:__lc_le=$global:LASTEXITCODE
function global:prompt {
  $ok=$global:?
  $s = if ($ok) {0} elseif ($global:LASTEXITCODE -ne $global:__lc_le -and $global:LASTEXITCODE) {$global:LASTEXITCODE} else {1}
  $global:__lc_le=$global:LASTEXITCODE
  $p = & $global:__lc_prompt
  "$($global:__lc_e)]133;D;$s;lc=$($global:__lc_n)$($global:__lc_b)$($global:__lc_e)]0;lcD$s$($global:__lc_b)" + $p
}
if (Test-Path Function:\PSConsoleHostReadLine) {
  $global:__lc_rl=$function:PSConsoleHostReadLine
  function global:PSConsoleHostReadLine {
    $l = & $global:__lc_rl
    [Console]::Write("$($global:__lc_e)]133;C;lc=$($global:__lc_n)$($global:__lc_b)")
    $global:__lc_le=$global:LASTEXITCODE
    $l
  }
  'readline: wrapped'
} else { 'readline: none' }
'psversion: ' + $PSVersionTable.PSVersion.ToString()
'psreadline: ' + ((Get-Module PSReadLine).Version)
'outputencoding: ' + [Console]::OutputEncoding.WebName
`

func encoded(script string) string {
	u := utf16.Encode([]rune(script))
	b := make([]byte, len(u)*2)
	for i, c := range u {
		b[2*i] = byte(c)
		b[2*i+1] = byte(c >> 8)
	}
	return base64.StdEncoding.EncodeToString(b)
}

type pty struct {
	hpc  windows.Handle
	in   *os.File
	out  *os.File
	proc windows.Handle
}

func start(cmdline string, c, r int) (*pty, error) {
	var inR, inW, outR, outW windows.Handle
	if err := windows.CreatePipe(&inR, &inW, nil, 0); err != nil {
		return nil, fmt.Errorf("input pipe: %w", err)
	}
	if err := windows.CreatePipe(&outR, &outW, nil, 0); err != nil {
		return nil, fmt.Errorf("output pipe: %w", err)
	}
	var hpc windows.Handle
	if err := windows.CreatePseudoConsole(windows.Coord{X: int16(c), Y: int16(r)}, inR, outW, 0, &hpc); err != nil {
		return nil, fmt.Errorf("CreatePseudoConsole: %w", err)
	}
	// The pseudo console holds its own copies of these.
	windows.CloseHandle(inR)
	windows.CloseHandle(outW)

	attrs, err := windows.NewProcThreadAttributeList(1)
	if err != nil {
		return nil, err
	}
	defer attrs.Delete()
	if err := attrs.Update(windows.PROC_THREAD_ATTRIBUTE_PSEUDOCONSOLE, *(*unsafe.Pointer)(unsafe.Pointer(&hpc)), unsafe.Sizeof(hpc)); err != nil {
		return nil, fmt.Errorf("attribute: %w", err)
	}
	si := windows.StartupInfoEx{ProcThreadAttributeList: attrs.List()}
	si.StartupInfo.Cb = uint32(unsafe.Sizeof(si))
	si.StartupInfo.Flags = windows.STARTF_USESTDHANDLES
	cmd16, err := windows.UTF16PtrFromString(cmdline)
	if err != nil {
		return nil, err
	}
	var pi windows.ProcessInformation
	if err := windows.CreateProcess(nil, cmd16, nil, nil, false,
		windows.EXTENDED_STARTUPINFO_PRESENT|windows.CREATE_UNICODE_ENVIRONMENT, nil, nil, &si.StartupInfo, &pi); err != nil {
		return nil, fmt.Errorf("CreateProcess: %w", err)
	}
	windows.CloseHandle(pi.Thread)
	return &pty{hpc: hpc, in: os.NewFile(uintptr(inW), "conpty-in"), out: os.NewFile(uintptr(outR), "conpty-out"), proc: pi.Process}, nil
}

var osc = regexp.MustCompile(`\x1b\](\d+);([^\x07\x1b]*)(\x07|\x1b\\)`)

func main() {
	flag.Parse()
	path := *shell
	args := []string{path, "-NoLogo", "-NoExit"}
	if !*profile {
		args = append(args, "-NoProfile")
	}
	args = append(args, "-EncodedCommand", encoded(integration))
	var v windows.OsVersionInfoEx
	maj, min, build := windows.RtlGetNtVersionNumbers()
	_ = v
	fmt.Printf("windows %d.%d.%d, shell %s, profile %v\n", maj, min, build, path, *profile)

	p, err := start(strings.Join(args, " "), *cols, *rows)
	if err != nil {
		fmt.Println("START FAILED:", err)
		os.Exit(1)
	}
	var mu sync.Mutex
	var stream []byte
	var times []string
	t0 := time.Now()
	done := make(chan struct{})
	go func() {
		defer close(done)
		buf := make([]byte, 32*1024)
		for {
			n, err := p.out.Read(buf)
			if n > 0 {
				mu.Lock()
				stream = append(stream, buf[:n]...)
				times = append(times, fmt.Sprintf("%6dms %5d bytes", time.Since(t0).Milliseconds(), n))
				mu.Unlock()
			}
			if err != nil {
				return
			}
		}
	}()
	quiet := func(d time.Duration, max time.Duration) {
		end := time.Now().Add(max)
		last := -1
		for time.Now().Before(end) {
			mu.Lock()
			n := len(stream)
			mu.Unlock()
			if n == last {
				return
			}
			last = n
			time.Sleep(d)
		}
	}
	quiet(1500*time.Millisecond, 20*time.Second)
	mark := func(label string) {
		mu.Lock()
		stream = append(stream, []byte("\n<<<"+label+">>>\n")...)
		mu.Unlock()
	}
	send := func(s string) {
		mark("SEND " + strings.TrimSpace(s))
		p.in.Write([]byte(s))
		quiet(700*time.Millisecond, 15*time.Second)
	}
	send("echo hello\r")
	send("cmd /c exit 3\r")
	send("Get-Item C:\\no\\such\\thing\r")
	send("'한글 출력'\r")
	send("1..3 | ForEach-Object { \"line $_\" }\r")
	send("$Host.UI.RawUI.WindowSize.Width\r")
	mark("RESIZE 80x20")
	windows.ResizePseudoConsole(p.hpc, windows.Coord{X: 80, Y: 20})
	quiet(700*time.Millisecond, 10*time.Second)
	send("$Host.UI.RawUI.WindowSize.Width\r")
	send("exit\r")
	ev, _ := windows.WaitForSingleObject(p.proc, 15000)
	var code uint32
	windows.GetExitCodeProcess(p.proc, &code)
	fmt.Printf("process wait %d, exit code %d\n", ev, code)
	closed := make(chan struct{})
	go func() { windows.ClosePseudoConsole(p.hpc); close(closed) }()
	select {
	case <-closed:
	case <-time.After(10 * time.Second):
		fmt.Println("ClosePseudoConsole did not return in 10s")
	}
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		fmt.Println("output did not end in 5s")
	}

	mu.Lock()
	defer mu.Unlock()
	s := string(stream)
	fmt.Println("==== OSC sequences")
	for _, m := range osc.FindAllStringSubmatch(s, -1) {
		fmt.Printf("OSC %s ; %q\n", m[1], m[2])
	}
	fmt.Printf("==== summary: OSC133 D=%d C=%d with nonce, title marks=%d, ESC[6n=%d\n",
		strings.Count(s, "133;D;"), strings.Count(s, "133;C;lc="+nonce), strings.Count(s, "]0;lcD"), strings.Count(s, "\x1b[6n"))
	fmt.Println("==== reads")
	for _, t := range times {
		fmt.Println(t)
	}
	fmt.Println("==== stream")
	fmt.Println(strings.NewReplacer("\x1b", "\\e", "\x07", "\\a", "\r", "\\r").Replace(s))
}
