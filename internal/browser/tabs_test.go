package browser

import (
	"errors"
	"testing"
)

// A conversation's tabs, as the panel's tab strip works them: a new tab is
// added and becomes the one the conversation works in, any of them can be
// made that one, and closing the active tab makes the one before it
// active.
func TestAConversationsTabsOpenSwitchAndClose(t *testing.T) {
	m := testManager(t)
	ctx := ctxFor(t)
	site := testSite(t)
	first, err := m.Tab(ctx, "c")
	if err != nil {
		t.Fatal(err)
	}
	if err := first.Navigate(ctx, site); err != nil {
		t.Fatal(err)
	}
	second, err := m.NewTab(ctx, "c")
	if err != nil {
		t.Fatal(err)
	}
	if err := second.Navigate(ctx, site+"/second"); err != nil {
		t.Fatal(err)
	}
	if m.Existing("c") != second {
		t.Fatal("a new tab is not the one the conversation works in")
	}
	tabs := m.Tabs(ctx, "c")
	if len(tabs) != 2 || tabs[0].Title != "Form" || tabs[1].Title != "Second" || tabs[0].Active || !tabs[1].Active {
		t.Fatalf("tabs = %+v", tabs)
	}
	// Another conversation's tabs are its own.
	if other := m.Tabs(ctx, "d"); other != nil {
		t.Errorf("a conversation with no tab lists %+v", other)
	}

	if err := m.ActivateTab("c", first.TargetID()); err != nil {
		t.Fatal(err)
	}
	if m.Existing("c") != first {
		t.Error("switching to the first tab did not make it the active one")
	}
	if err := m.ActivateTab("c", "no-such-tab"); !errors.Is(err, ErrNoSuchTab) {
		t.Errorf("switching to a tab that is not there: %v", err)
	}
	if err := m.ActivateTab("d", first.TargetID()); !errors.Is(err, ErrNoSuchTab) {
		t.Errorf("another conversation switched to this one's tab: %v", err)
	}

	// Closing the active tab, the first, leaves the second active.
	if err := m.CloseTabID(ctx, "c", first.TargetID()); err != nil {
		t.Fatal(err)
	}
	if m.Existing("c") != second {
		t.Error("closing the active tab did not leave the other one active")
	}
	if tabs := m.Tabs(ctx, "c"); len(tabs) != 1 || tabs[0].ID != second.TargetID() || !tabs[0].Active {
		t.Errorf("after closing one, tabs = %+v", tabs)
	}
	if err := m.CloseTabID(ctx, "c", first.TargetID()); !errors.Is(err, ErrNoSuchTab) {
		t.Errorf("closing a closed tab again: %v", err)
	}
	// Closing the last leaves the conversation with none.
	if err := m.CloseTabID(ctx, "c", second.TargetID()); err != nil {
		t.Fatal(err)
	}
	if m.Existing("c") != nil || m.Tabs(ctx, "c") != nil {
		t.Error("a conversation whose last tab closed still has one")
	}
}

// Closing a tab that is not the active one leaves the active one where it
// is, and the one before the active tab stays active when it closes.
func TestClosingATabKeepsTheActiveOneInPlace(t *testing.T) {
	m := testManager(t)
	ctx := ctxFor(t)
	a, err := m.Tab(ctx, "c")
	if err != nil {
		t.Fatal(err)
	}
	b, err := m.NewTab(ctx, "c")
	if err != nil {
		t.Fatal(err)
	}
	c, err := m.NewTab(ctx, "c")
	if err != nil {
		t.Fatal(err)
	}
	if err := m.ActivateTab("c", b.TargetID()); err != nil {
		t.Fatal(err)
	}
	if err := m.CloseTabID(ctx, "c", a.TargetID()); err != nil {
		t.Fatal(err)
	}
	if m.Existing("c") != b {
		t.Error("closing a tab before the active one moved the active one")
	}
	if err := m.CloseTabID(ctx, "c", c.TargetID()); err != nil {
		t.Fatal(err)
	}
	if m.Existing("c") != b {
		t.Error("closing a tab after the active one moved the active one")
	}
}
