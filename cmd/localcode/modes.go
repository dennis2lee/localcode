package main

import (
	"context"
	"fmt"
	"log"
	"net"
	"net/http"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"localcode/internal/client"
	"localcode/internal/dialog"
	"localcode/internal/gui"
	"localcode/internal/tui"
)

func runDaemon(configPath, listen string) error {
	d, cleanup, err := buildDaemon(context.Background(), configPath, nil)
	if err != nil {
		return err
	}
	defer cleanup()
	log.Printf("localcode daemon listening on http://%s", listen)
	return http.ListenAndServe(listen, d.Handler())
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

	// "LocalCode", not "localcode": this string is the window title and
	// the taskbar label, where it is the product's name rather than the
	// command you type. The binary, the package and the CLI stay
	// lower-case.
	return gui.Launch("LocalCode", version, func(progress func(string)) (http.Handler, error) {
		d, done, err := buildDaemon(context.Background(), configPath, progress)
		if err != nil {
			return nil, err
		}
		cleanup = done
		// Same reasoning as the picker below, one step stronger: installing
		// replaces the program on the machine the daemon runs on, so the
		// button only exists where that machine is the one being looked at.
		d.AllowUpdateInstall = true
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
		return d.Handler(), nil
	})
}

// runEmbedded starts a daemon in-process (so a browser can also point at
// the same --listen address for the Web UI) and attaches a TUI client to
// it over real HTTP/SSE — the TUI and daemon are still separate,
// independently-addressable components, just sharing a process for
// single-binary convenience.
func runEmbedded(configPath, listen, agentName string) error {
	d, cleanup, err := buildDaemon(context.Background(), configPath, nil)
	if err != nil {
		return err
	}
	defer cleanup()

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

	srv := &http.Server{Addr: listen, Handler: d.Handler()}
	errCh := make(chan error, 1)
	go func() { errCh <- srv.ListenAndServe() }()

	// Give the listener a moment to come up before the client dials it.
	select {
	case err := <-errCh:
		return fmt.Errorf("daemon failed to start: %w", err)
	case <-time.After(150 * time.Millisecond):
	}

	return runTUIClient("http://"+listen, agentName)
}

func runTUIClient(serverURL, agentName string) error {
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
