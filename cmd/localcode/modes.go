package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	tea "charm.land/bubbletea/v2"

	"localcode/internal/client"
	"localcode/internal/dialog"
	"localcode/internal/gui"
	"localcode/internal/tui"
	"localcode/internal/update"
)

func runDaemon(configPath, listen string) error {
	// A listener of our own rather than ListenAndServe, because a
	// handoff has to be able to take it: the socket is what the next
	// version inherits.
	ln, err := net.Listen("tcp", listen)
	if err != nil {
		return fmt.Errorf("daemon failed to start: %w", err)
	}

	d, cleanup, err := buildDaemon(context.Background(), configPath, nil)
	if err != nil {
		ln.Close()
		return err
	}
	var cleanupOnce sync.Once
	cleanupOnce_ := func() { cleanupOnce.Do(cleanup) }
	defer cleanupOnce_()

	// Before the listener serves, which is the whole point: a headless
	// daemon with nothing bound has no client attached and no session
	// open, so replacing it costs nobody a turn. Once it is serving, the
	// same update is somebody's work — and a handoff.
	//
	// Two ways the new version comes up. Where this process can exec, it
	// does, and this function never returns. Where it cannot — Windows —
	// the new binary is started beside this process on this listener,
	// and this process stays only to hold the console: Ctrl+C here ends
	// both, through the pipe the successor watches.
	if binary, ok := startupHandoffBinary(d, os.Stderr); ok {
		cleanupOnce_()
		return superviseSuccessor(binary, ln)
	}

	srv := &http.Server{Handler: d.Handler()}
	d.Shutdown = func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(ctx)
	}
	handedOff := make(chan struct{}, 1)
	d.Handoff = func(version, binary string) error {
		if err := handoffTo(d, srv, ln, nil, cleanupOnce_, version, binary, nil); err != nil {
			return err
		}
		handedOff <- struct{}{}
		return nil
	}

	log.Printf("localcode daemon listening on http://%s", ln.Addr())
	errCh := make(chan error, 1)
	go func() { errCh <- srv.Serve(ln) }()
	err = <-errCh
	select {
	case <-handedOff:
		// Serve returned because the listener was closed for the
		// successor. This process has finished its turns and is done.
		return nil
	default:
	}
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}

// runGUI opens a native window (see internal/gui) and builds the daemon
// behind its startup screen, handing over the HTTP handler when it is
// ready. No fixed port, no browser: the window owns a private loopback
// server for its own lifetime. On a build without the "gui" tag,
// gui.Launch returns an explanatory error instead.
//
// The window is created before any of this work rather than after, so
// clicking the icon produces something to look at immediately — see
// gui.Launch. That is also why cleanup is registered from inside the
// callback: there is nothing to clean up until the build succeeds, and
// by then runGUI is already blocked in Launch.
func runGUI(configPath string) error {
	var cleanup func()
	defer func() {
		if cleanup != nil {
			cleanup()
		}
	}()

	alive, err := processAlivePipe()
	if err != nil {
		return err
	}

	// "LocalCode", not "localcode": this string is the window title and
	// the taskbar label, where it is the product's name rather than the
	// command you type. The binary, the package and the CLI stay
	// lower-case.
	return gui.Launch("LocalCode", version, func(progress func(string), reload func()) (http.Handler, error) {
		d, done, err := buildDaemon(context.Background(), configPath, progress)
		if err != nil {
			return nil, err
		}
		cleanup = done
		var cleanupOnce sync.Once
		cleanupOnce_ := func() { cleanupOnce.Do(done) }
		// Same reasoning as the picker below, one step stronger: installing
		// replaces the program on the machine the daemon runs on, so the
		// button only exists where that machine is the one being looked at.
		d.AllowUpdateInstall = true
		// Whether the installer's close is followed by a return. See
		// internal/gui/restart_windows.go; the reply to the install button
		// reads it.
		d.InstallerRestarts = gui.InstallerRestarts()

		// The startup update, where exec is not available. The new version
		// is started beside this process on a loopback listener of its
		// own, and the window fronts it through the proxy: the page is
		// served by the new version, so it is the new interface, and the
		// two routes that open native dialogs stay here. This was
		// described in v0.88.0 and not wired; the window built its daemon
		// in-process every start and never asked.
		if binary, ok := startupHandoffBinary(d, os.Stderr); ok {
			// The splash shows this shell's own version, which after an
			// update is the copy the shortcut points at rather than the
			// one about to run. Saying which version is coming up is the
			// difference between "it did not update" and "it did".
			coming := ""
			if v, verr := update.VersionOf(binary); verr == nil {
				coming = v
				progress("starting localcode " + v)
			}
			ln, lerr := net.Listen("tcp", "127.0.0.1:0")
			if lerr != nil {
				return nil, fmt.Errorf("bind a port for the new localcode: %w", lerr)
			}
			// spawnAndWatch, not spawnSuccessor: this branch used to throw
			// the exit channel away, so a window that updated at startup
			// could never say why its daemon died — every 502 took the
			// "still running, as far as this process knows" branch, which
			// was exactly the case it was not.
			if _, serr := spawnAndWatch(binary, ln, alive.r); serr != nil {
				ln.Close()
				// On the splash, not only on a stderr nobody is reading:
				// the line above has just promised the new version, and
				// this is the window quietly coming up on the old one.
				if coming != "" {
					progress("localcode " + coming + " would not start; running " + version + " instead")
				}
				fmt.Fprintf(os.Stderr, "start the new localcode: %v; running this version instead\n", serr)
			} else {
				addr := ln.Addr().String()
				ln.Close()
				cleanupOnce_()
				cleanup = nil
				return successorProxy(addr), nil
			}
		}

		// Only here. The window and the daemon share a machine and a user
		// in this mode, so a folder picker opens where the person clicking
		// is sitting. Every other mode leaves d.PickDirectory nil — over
		// --server (or a browser on another box) the dialog would open on
		// the daemon's machine, in front of nobody.
		if dialog.Available() {
			d.PickDirectory = func(ctx context.Context, startDir string) (string, error) {
				return dialog.PickDirectory(ctx, "Choose a workspace folder", startDir)
			}
			// Same machine, same rule: the folder icon in the header opens
			// an Explorer/Finder window, which is only useful in front of
			// the person who clicked it.
			d.RevealDirectory = dialog.RevealDirectory
		}

		// "/update" hands the daemon behind the window to the new binary
		// the way the terminal does, through the proxy above. The window
		// stays; the page is reloaded onto the new version; work in flight
		// finishes in the old daemon first. Before this the window had no
		// Handoff and took the installer path, which on Windows closed
		// localcode and left it closed.
		front := newSwapHandler(d.Handler())
		d.Handoff = func(version, binary string) error {
			return windowHandoff(d, front, alive, cleanupOnce_, reload, version, binary)
		}
		return front, nil
	})
}

// runEmbedded starts a daemon in-process (so a browser can also point at
// the same --listen address for the Web UI) and attaches a TUI client to
// it over real HTTP/SSE — the TUI and daemon are still separate,
// independently-addressable components, just sharing a process for
// single-binary convenience.
func runEmbedded(configPath, listen, agentName string, listenExplicit bool) error {
	// A process started by a handoff has no terminal of its own to draw
	// in: the TUI is still running in the process that started it. What
	// it has is that process's listener, and it serves that until the
	// terminal goes away.
	if in, ok := takingOver(); ok {
		return runSuccessor(configPath, in)
	}

	// The address is taken before the daemon is built, because one of
	// the answers is "there is already a daemon there", and building a
	// second one to throw away would start MCP servers and load skills
	// for a process that is about to be a client.
	// The directory this terminal is in decides whether an already
	// running daemon is one to attach to: a daemon stamps its own
	// directory onto every session created on it, so attaching from
	// another project would open a conversation editing the wrong files.
	workdir, _ := os.Getwd()

	got, err := takeListener(listen, workdir, listenExplicit)
	if err != nil {
		return fmt.Errorf("daemon failed to start: %w", err)
	}
	if got.attachTo != "" {
		fmt.Printf("attaching to the localcode daemon already running at %s\n", got.attachTo)
		// No restart hook, for the same reason --server has none: that
		// daemon is somebody else's process and not ours to replace.
		return runTUIClient(got.attachTo, agentName, nil)
	}
	if got.otherDaemon {
		// A localcode is there, and it works somewhere else.
		ln, lerr := bindElsewhere(listen)
		if lerr != nil {
			return fmt.Errorf("daemon failed to start: %w", lerr)
		}
		where := got.elsewhere
		if where == "" {
			where = "another directory"
		}
		got.ln, got.moved = ln, true
		fmt.Printf("a localcode at %s is working in %s, so this one started its own\n", listen, where)
	}
	listen = got.ln.Addr().String()
	if got.moved {
		fmt.Printf("the Web UI for this one is at http://%s\n", listen)
	}

	d, cleanup, err := buildDaemon(context.Background(), configPath, nil)
	if err != nil {
		got.ln.Close()
		return err
	}
	// Once, whichever path gets there first: a handoff runs it when the
	// retiring daemon has drained, and the deferred call at exit must
	// not run it again.
	var cleanupOnce sync.Once
	cleanupOnce_ := func() { cleanupOnce.Do(cleanup) }
	defer cleanupOnce_()

	// The update button, on the same rule as the desktop window: it
	// replaces the program on the machine the daemon runs on, so it exists
	// only where that machine is the one the person clicking is sitting
	// at. A daemon on loopback was started by this user, on this machine,
	// with a TUI attached to it in this terminal — that is that machine.
	// Exposing it deliberately (--listen 0.0.0.0:4096) is the case where
	// the browser could be anywhere, and there the button goes away.
	//
	// This is what makes an install under someone's home directory
	// self-updating on Linux, where there is no desktop window to click
	// in: the binary is the user's own, so replacing it needs nobody's
	// password.
	d.AllowUpdateInstall = loopbackOnly(listen)

	// And before the TUI, for the same reason the headless path does it
	// before its listener: nothing has been drawn, no conversation has
	// been opened, and the exec below costs a moment of startup rather
	// than somebody's turn. It is also the one place the terminal does
	// not have to be handed back first — the TUI has not taken it yet.
	autoUpdateAtStartup(d, os.Stderr)

	// Where exec is not available, the new version comes up beside this
	// process instead: it takes the listener before anything is served,
	// and this process runs the TUI against it — which is exactly the
	// state a "/update" handoff leaves things in, arrived at from the
	// start. The daemon built above is torn down; the successor builds
	// its own.
	if binary, ok := startupHandoffBinary(d, os.Stderr); ok {
		cleanupOnce_()
		return runTUIBehindSuccessor(binary, got.ln, listen, agentName)
	}

	// Coming back up on the new binary once an update has replaced it.
	//
	// Quit first, exec after. The TUI owns the terminal — raw mode, the
	// alternate screen — and exec'ing out from under it would leave the
	// new program to inherit a terminal the old one never put back. So
	// the update asks the program to stop, p.Run returns the way it does
	// for any other exit, and the exec happens on a terminal that is
	// already the user's again.
	restart := make(chan struct{}, 1)
	var prog atomic.Pointer[tea.Program]
	if d.AllowUpdateInstall {
		d.Restart = func() {
			select {
			case restart <- struct{}{}:
			default:
			}
			if p := prog.Load(); p != nil {
				p.Quit()
			}
		}
	}

	// Serve, not ListenAndServe: the listener is already bound, which is
	// what let the decision above be made before anything was built.
	// The pipe every daemon under this terminal watches: its read end is
	// inherited by each successor, and the write end closes when this
	// process — the one with the TUI — exits. See handoff_unix.go.
	//
	// The process's own, not one made here. This one happened to survive,
	// because the Handoff closure below captures it and that closure lives
	// as long as the daemon does — but that is an accident of where it is
	// used rather than a lifetime anybody chose, and it is the same
	// accident the other three paths did not have. See processAlivePipe.
	alive, err := processAlivePipe()
	if err != nil {
		got.ln.Close()
		return fmt.Errorf("daemon failed to start: %w", err)
	}

	srv := &http.Server{Handler: d.Handler()}
	// Set when this process has handed its daemon to a successor and is
	// now only the TUI. On exit it then stops that successor, which has
	// nobody else.
	handedOff := make(chan struct{}, 1)
	if d.AllowUpdateInstall {
		d.Handoff = func(version, binary string) error {
			// Signalled from inside, the moment the successor is
			// answering and before the listener is closed: the goroutine
			// watching Serve unblocks on that close, and without knowing
			// the handoff is under way it treated the return as fatal and
			// took the process down mid-drain.
			marked := func() {
				select {
				case handedOff <- struct{}{}:
				default:
				}
			}
			if err := handoffTo(d, srv, got.ln, alive.r, cleanupOnce_, version, binary, marked); err != nil {
				return err
			}
			return nil
		}
	}
	errCh := make(chan error, 1)
	go func() { errCh <- srv.Serve(got.ln) }()

	// Give the listener a moment to come up before the client dials it.
	select {
	case err := <-errCh:
		return fmt.Errorf("daemon failed to start: %w", err)
	case <-time.After(150 * time.Millisecond):
	}

	err = runTUIClient("http://"+listen, agentName, &prog)
	select {
	case <-handedOff:
		// The daemon under this TUI is another process now, and this
		// TUI was the only thing keeping it. Asked to stop rather than
		// left to notice the pipe closing, because a request is prompt
		// and a pipe closing is only eventually noticed; the pipe stays
		// as the backstop for a TUI that crashed. The write end closes
		// with this process.
		if serr := stopDaemonAt(listen); serr != nil {
			fmt.Fprintf(os.Stderr, "the daemon at %s was not stopped: %v\n", listen, serr)
		}
		return err
	case <-restart:
		// Everything this process owns goes with the exec, so the daemon's
		// cleanup runs here rather than from the deferred call above,
		// which the exec would never reach.
		cleanupOnce_()
		return execSelf()
	default:
		return err
	}
}

// runSuccessor is the embedded mode's daemon half alone, in a process
// that was handed a listener by the one with the TUI. It serves until
// asked to stop, or until the terminal it was started under goes away.
func runSuccessor(configPath string, in inherited) error {
	d, cleanup, err := buildDaemon(context.Background(), configPath, nil)
	if err != nil {
		in.ln.Close()
		return err
	}
	var cleanupOnce sync.Once
	cleanupOnce_ := func() { cleanupOnce.Do(cleanup) }
	defer cleanupOnce_()

	d.NoteTakeover()
	listen := in.ln.Addr().String()
	d.AllowUpdateInstall = loopbackOnly(listen)
	// Whether an installer's close is followed by a return. Only the
	// process with the window knows — it is the one that registered for
	// it — and after a handoff that process is not this one, so it says
	// so on the command line. Without it the install reply dropped the
	// sentence promising Windows would start localcode again, in exactly
	// the mode where the promise is kept.
	d.InstallerRestarts = os.Getenv(envInstallerRestarts) == "1"

	srv := &http.Server{Handler: d.Handler()}
	d.Shutdown = func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(ctx)
	}
	handedOff := make(chan struct{}, 1)
	if d.AllowUpdateInstall {
		d.Handoff = func(version, binary string) error {
			// Signalled from inside, the moment the successor is
			// answering and before the listener is closed: the goroutine
			// watching Serve unblocks on that close, and without knowing
			// the handoff is under way it treated the return as fatal and
			// took the process down mid-drain.
			marked := func() {
				select {
				case handedOff <- struct{}{}:
				default:
				}
			}
			if err := handoffTo(d, srv, in.ln, in.alive, cleanupOnce_, version, binary, marked); err != nil {
				return err
			}
			return nil
		}
	}

	errCh := make(chan error, 1)
	go func() { errCh <- srv.Serve(in.ln) }()
	in.announceReady()
	err = <-errCh
	select {
	case <-handedOff:
		return nil
	default:
	}
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}

// runTUIClient attaches a TUI to a daemon. prog, when not nil, is handed
// the running program so something outside this function can end it — the
// update path, which has to bring the terminal back before it restarts.
func runTUIClient(serverURL, agentName string, prog *atomic.Pointer[tea.Program]) error {
	c := client.New(serverURL)

	ctx := context.Background()
	sess, err := pickOrCreateSession(ctx, c, agentName)
	if err != nil {
		return fmt.Errorf("create session on %s: %w", serverURL, err)
	}

	// StreamEvents, not SubscribeEvents: the daemon ends a stream that has
	// fallen behind rather than skipping events on it, so a client that
	// does not reconnect and resume would sit on a half-finished reply.
	eventCh := c.StreamEvents(ctx, sess.ID, 0)

	model := tui.New(c, sess.ID, sess.Agent, eventCh)
	p := tea.NewProgram(model)
	if prog != nil {
		prog.Store(p)
	}
	_, err = p.Run()
	return err
}

// loopbackOnly reports whether an address is reachable only from this
// machine. An empty or wildcard host is every interface, which is the
// answer "no" — and so is anything that does not parse, because a
// permission granted on a string nobody understood is not one.
func loopbackOnly(listen string) bool {
	host, _, err := net.SplitHostPort(listen)
	if err != nil {
		host = listen
	}
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(strings.Trim(host, "[]"))
	return ip != nil && ip.IsLoopback()
}
