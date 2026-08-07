package main

import (
	"context"
	"fmt"
	"log"
	"net/http"
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
		// Only here. The window and the daemon share a machine and a user
		// in this mode, so a folder picker opens where the person clicking
		// is sitting. Every other mode leaves d.PickDirectory nil — over
		// --server (or a browser on another box) the dialog would open on
		// the daemon's machine, in front of nobody.
		if dialog.Available() {
			d.PickDirectory = func(ctx context.Context, startDir string) (string, error) {
				return dialog.PickDirectory(ctx, "Choose a workspace folder", startDir)
			}
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

	eventCh, err := c.SubscribeEvents(ctx, sess.ID, 0)
	if err != nil {
		return fmt.Errorf("subscribe to events: %w", err)
	}

	model := tui.New(c, sess.ID, sess.Agent, eventCh)
	p := tea.NewProgram(model)
	_, err = p.Run()
	return err
}
