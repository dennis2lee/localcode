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
	d, cleanup, err := buildDaemon(context.Background(), configPath)
	if err != nil {
		return err
	}
	defer cleanup()
	log.Printf("localcode daemon listening on http://%s", listen)
	return http.ListenAndServe(listen, d.Handler())
}

// runGUI builds the daemon in-process and hands its HTTP handler to a
// native window (see internal/gui). No fixed port, no browser: the window
// owns a private loopback server for its own lifetime. On a build without
// the "gui" tag, gui.Launch returns an explanatory error instead.
func runGUI(configPath string) error {
	d, cleanup, err := buildDaemon(context.Background(), configPath)
	if err != nil {
		return err
	}
	defer cleanup()
	// Only here. The window and the daemon share a machine and a user in
	// this mode, so a folder picker opens where the person clicking is
	// sitting. Every other mode leaves d.PickDirectory nil — over --server
	// (or a browser on another box) the dialog would open on the daemon's
	// machine, in front of nobody.
	if dialog.Available() {
		d.PickDirectory = func(ctx context.Context, startDir string) (string, error) {
			return dialog.PickDirectory(ctx, "Choose a workspace folder", startDir)
		}
	}
	return gui.Launch("localcode", d.Handler())
}

// runEmbedded starts a daemon in-process (so a browser can also point at
// the same --listen address for the Web UI) and attaches a TUI client to
// it over real HTTP/SSE — the TUI and daemon are still separate,
// independently-addressable components, just sharing a process for
// single-binary convenience.
func runEmbedded(configPath, listen, agentName string) error {
	d, cleanup, err := buildDaemon(context.Background(), configPath)
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
