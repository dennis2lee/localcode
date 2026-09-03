package daemon

import (
	"context"
	"errors"
	"strings"
	"testing"

	"localcode/internal/agent"
	"localcode/internal/config"
)

// Whether work in flight blocks an update depends on one thing: whether
// the daemon can hand itself over instead of restarting. With a handoff
// the work finishes where it is and is no reason to wait; without one a
// restart would cut it, so it is refused, by name.
func TestBusyIsARefusalOnlyWhereThereIsNoHandoff(t *testing.T) {
	busy := []string{"S2"}
	if err := updateWhileBusy(true, busy, nil); err != nil {
		t.Errorf("a daemon that can hand off refused for a busy conversation: %v", err)
	}
	if err := updateWhileBusy(true, nil, []string{"task-1"}); err != nil {
		t.Errorf("a daemon that can hand off refused for a background task: %v", err)
	}
	if err := updateWhileBusy(false, busy, nil); err == nil || !strings.Contains(err.Error(), "S2") {
		t.Errorf("without a handoff, a busy conversation should be refused by name; got %v", err)
	}
	if err := updateWhileBusy(false, nil, []string{"task-1"}); err == nil || !strings.Contains(err.Error(), "task-1") {
		t.Errorf("without a handoff, a background task should be refused by name; got %v", err)
	}
	if err := updateWhileBusy(false, nil, nil); err != nil {
		t.Errorf("an idle daemon with no handoff refused: %v", err)
	}
}

// "/update" replaces the program for every conversation on this daemon,
// not just the one it was typed in. So the question it has to ask is a
// daemon-wide one, and the answer has to name what it found: "something
// is running" sends somebody looking through a session list, and the
// point of asking at all was to not make them guess.
func TestUpdateRefusesWhileAnotherConversationHasATurn(t *testing.T) {
	d := &Daemon{
		Version:            "0.1.0",
		AllowUpdateInstall: true,
		Loop:               &agent.Loop{Config: &config.Config{}},
		turns:              newTurnTracker(),
	}
	_, cancel := context.WithCancel(context.Background())
	defer cancel()
	if !d.turns.begin("S2", cancel) {
		t.Fatal("could not mark S2 busy")
	}

	_, err := d.SelfUpdate("S1")
	if err == nil {
		t.Fatal("update went ahead with a turn running in another conversation")
	}
	if !strings.Contains(err.Error(), "S2") {
		t.Errorf("the refusal does not name what is running: %v", err)
	}
}

// The turn recorded against the session that typed "/update" is that
// command. Counting it would make the command refuse itself, every time,
// with a message naming the conversation the person is looking at.
func TestUpdateDoesNotCountItsOwnTurn(t *testing.T) {
	d := &Daemon{
		Version:            "0.1.0",
		AllowUpdateInstall: true,
		// Pointed at a closed port rather than left unset: unset means
		// GitHub, and a test that reaches the internet is not one. The
		// check failing is fine — this is about what happens before it.
		UpdateAPI: "http://127.0.0.1:1/releases",
		Loop:      &agent.Loop{Config: &config.Config{}},
		turns:     newTurnTracker(),
	}
	_, cancel := context.WithCancel(context.Background())
	defer cancel()
	if !d.turns.begin("S1", cancel) {
		t.Fatal("could not mark S1 busy")
	}

	_, err := d.SelfUpdate("S1")
	if err != nil && strings.Contains(err.Error(), "turn in progress") {
		t.Errorf("/update refused itself: %v", err)
	}
}

// A daemon nobody may install onto says so rather than reaching for the
// network first. The wording points somewhere the person can actually get
// it, because "cannot" without "here instead" is not an answer.
func TestUpdateSaysSoWhereItCannotInstall(t *testing.T) {
	d := &Daemon{
		Version:            "0.1.0",
		AllowUpdateInstall: false,
		Loop:               &agent.Loop{Config: &config.Config{}},
	}
	_, err := d.SelfUpdate("S1")
	if err == nil {
		t.Fatal("expected a refusal")
	}
	if !strings.Contains(err.Error(), "github.com") {
		t.Errorf("the refusal does not say where to get it: %v", err)
	}
	// And that this daemon does still update itself, at the one moment it
	// is allowed to. Without that line the two behaviours read as a
	// contradiction to somebody who watched it update at startup.
	if !strings.Contains(err.Error(), "at startup") {
		t.Errorf("the refusal does not mention the startup update, which this daemon does do: %v", err)
	}
}

// Startup asks a different question from the button, and the difference
// is who is asking. AllowUpdateInstall answers "may a browser somewhere
// else replace the program on this machine", which is no for a daemon on
// a public address. At startup nobody remote is asking: the machine is
// deciding about itself, and gating that on the remote-install flag would
// mean a headless daemon — the install that most wants this — never
// updated.
func TestStartupIsNotGatedOnTheRemoteInstallFlag(t *testing.T) {
	d := &Daemon{
		Version:            "0.1.0",
		AllowUpdateInstall: false,
		Loop:               &agent.Loop{Config: &config.Config{}},
		// Pointed at nothing, so the check fails rather than reaching
		// GitHub from a test. What matters is which failure: a refusal
		// would mean the flag stopped it before it ever looked.
		UpdateAPI: "http://127.0.0.1:1/releases",
	}
	_, err := d.InstallAtStartup(context.Background())
	if err == nil {
		t.Fatal("expected the unreachable check to fail")
	}
	if errors.Is(err, ErrNoUpdate) {
		t.Error("startup treated a dead endpoint as 'nothing to install', which hides a broken update path")
	}
	if strings.Contains(err.Error(), "cannot install updates") {
		t.Errorf("startup was refused by the remote-install flag: %v", err)
	}
}
