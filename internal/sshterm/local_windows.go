//go:build windows

package sshterm

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"sync"
	"unsafe"

	"golang.org/x/sys/windows"
)

// startPseudoConsole starts argv under a Windows pseudo console (ConPTY)
// of cols by rows.
//
// The shell is attached to the pseudo console and to nothing else: no
// window, in the desktop window's GUI build too, and none of localcode's
// own standard handles (STARTF_USESTDHANDLES with none given), which a
// child would otherwise inherit where localcode was started from a
// terminal. Its environment is localcode's own.
func startPseudoConsole(argv []string, dir string, cols, rows int) (LocalProcess, error) {
	if len(argv) == 0 {
		return nil, errors.New("no shell")
	}
	exe, err := exec.LookPath(argv[0])
	if err != nil {
		return nil, fmt.Errorf("%s: %w", argv[0], err)
	}
	var inR, inW, outR, outW windows.Handle
	if err := windows.CreatePipe(&inR, &inW, nil, 0); err != nil {
		return nil, fmt.Errorf("input pipe: %w", err)
	}
	if err := windows.CreatePipe(&outR, &outW, nil, 0); err != nil {
		windows.CloseHandle(inR)
		windows.CloseHandle(inW)
		return nil, fmt.Errorf("output pipe: %w", err)
	}
	var hpc windows.Handle
	if err := windows.CreatePseudoConsole(windows.Coord{X: int16(cols), Y: int16(rows)}, inR, outW, 0, &hpc); err != nil {
		for _, h := range []windows.Handle{inR, inW, outR, outW} {
			windows.CloseHandle(h)
		}
		return nil, fmt.Errorf("pseudo console: %w", err)
	}
	// The pseudo console holds its own copies of these two.
	windows.CloseHandle(inR)
	windows.CloseHandle(outW)
	in := os.NewFile(uintptr(inW), "pseudo-console-input")
	out := os.NewFile(uintptr(outR), "pseudo-console-output")
	// Before the shell starts: nothing reads the output, and closing a
	// pseudo console waits for its last frame to be read before Windows
	// 11 24H2, or for the pipe to break. The pipe is closed first.
	fail := func(err error) (LocalProcess, error) {
		in.Close()
		out.Close()
		windows.ClosePseudoConsole(hpc)
		return nil, err
	}

	attrs, err := windows.NewProcThreadAttributeList(1)
	if err != nil {
		return fail(err)
	}
	defer attrs.Delete()
	// The attribute's value is the pseudo console handle itself, not a
	// pointer to it.
	if err := attrs.Update(windows.PROC_THREAD_ATTRIBUTE_PSEUDOCONSOLE, *(*unsafe.Pointer)(unsafe.Pointer(&hpc)), unsafe.Sizeof(hpc)); err != nil {
		return fail(fmt.Errorf("pseudo console attribute: %w", err))
	}
	si := windows.StartupInfoEx{ProcThreadAttributeList: attrs.List()}
	si.StartupInfo.Cb = uint32(unsafe.Sizeof(si))
	si.StartupInfo.Flags = windows.STARTF_USESTDHANDLES
	app, err := windows.UTF16PtrFromString(exe)
	if err != nil {
		return fail(err)
	}
	line, err := windows.UTF16PtrFromString(windows.ComposeCommandLine(argv))
	if err != nil {
		return fail(err)
	}
	var cwd *uint16
	if dir != "" {
		if cwd, err = windows.UTF16PtrFromString(dir); err != nil {
			return fail(err)
		}
	}
	allowCtrlC.Do(clearIgnoreCtrlC)
	var pi windows.ProcessInformation
	if err := windows.CreateProcess(app, line, nil, nil, false,
		windows.EXTENDED_STARTUPINFO_PRESENT|windows.CREATE_UNICODE_ENVIRONMENT, nil, cwd, &si.StartupInfo, &pi); err != nil {
		return fail(fmt.Errorf("start %s: %w", exe, err))
	}
	windows.CloseHandle(pi.Thread)
	return &conpty{hpc: hpc, in: in, out: out, proc: pi.Process}, nil
}

// A process started with "ignore Ctrl+C" set passes it on to every
// process it starts, and a shell with it set takes Ctrl+C from its console
// as nothing: the key the person, or a run that timed out, presses to stop
// a command stopped nothing. localcode has it set whenever whatever
// started it asked for a new process group, which a program started from
// Git Bash gets (measured: the Windows tests run under bash on CI, and a
// Start-Sleep under them ignored Ctrl+C that one started from pwsh took).
// So it is cleared, once, before the first shell starts. In the desktop
// window this changes nothing else: it has no console of its own. In a
// console it makes Ctrl+C there reach localcode, as it does for a
// localcode started from anywhere else.
var (
	allowCtrlC                = sync.Once{}
	procSetConsoleCtrlHandler = windows.NewLazySystemDLL("kernel32.dll").NewProc("SetConsoleCtrlHandler")
)

func clearIgnoreCtrlC() {
	_, _, _ = procSetConsoleCtrlHandler.Call(0, 0)
}

// conpty is a shell under a pseudo console.
type conpty struct {
	hpc  windows.Handle
	in   *os.File
	out  *os.File
	proc windows.Handle

	// mu keeps Resize off a pseudo console that Close has begun to
	// close: the handle is freed by ClosePseudoConsole.
	mu        sync.Mutex
	closed    bool
	closeOnce sync.Once
	waitOnce  sync.Once
	code      int
	waitErr   error
}

func (c *conpty) Read(p []byte) (int, error)  { return c.out.Read(p) }
func (c *conpty) Write(p []byte) (int, error) { return c.in.Write(p) }

func (c *conpty) Resize(cols, rows int) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return errors.New("the pseudo console is closed")
	}
	return windows.ResizePseudoConsole(c.hpc, windows.Coord{X: int16(cols), Y: int16(rows)})
}

func (c *conpty) Wait() (int, error) {
	c.waitOnce.Do(func() {
		if _, err := windows.WaitForSingleObject(c.proc, windows.INFINITE); err != nil {
			c.waitErr = err
			return
		}
		var code uint32
		if err := windows.GetExitCodeProcess(c.proc, &code); err != nil {
			c.waitErr = err
			return
		}
		c.code = int(code)
	})
	return c.code, c.waitErr
}

// Close ends the shell: the pseudo console first, which ends every
// program attached to it as closing a console window does, then the
// shell itself if it is still there.
//
// ClosePseudoConsole can wait until its output has been read, on some
// versions of Windows: the terminal's reader goes on reading until the
// output ends, and the call is not waited for here.
func (c *conpty) Close() error {
	c.closeOnce.Do(func() {
		c.mu.Lock()
		c.closed = true
		c.mu.Unlock()
		done := make(chan struct{})
		go func() {
			windows.ClosePseudoConsole(c.hpc)
			close(done)
		}()
		c.in.Close()
		_ = windows.TerminateProcess(c.proc, 1)
		go func() {
			<-done
			c.out.Close()
			// Wait has its own copy of the handle's use; it returns
			// once the process is gone, which it now is.
			c.Wait()
			windows.CloseHandle(c.proc)
		}()
	})
	return nil
}
