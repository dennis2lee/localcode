package session

import (
	"errors"
	"reflect"
	"strings"
	"testing"
)

// A failed metadata write must leave the session exactly as it was.
// The file is the record: what a restart would restore has to be what
// the store claims, so a write that fails cannot leave a value behind
// in memory. These tests fail the write through the store's hook rather
// than through the filesystem, because an unwritable directory needs a
// non-root POSIX account and behaves differently on Windows.

// errInjectedWrite is the failure the hook reports. Setters must return
// it unchanged: the caller sees the write error, not a wrapped copy.
var errInjectedWrite = errors.New("injected meta write failure")

func failMetaWrites(s *Store) {
	s.writeMeta = func(string, Session) error { return errInjectedWrite }
}

func mustCreateRollbackSession(t *testing.T, s *Store, id, parent, agent string, visible bool) {
	t.Helper()
	if _, err := s.CreateSession(id, parent, agent, visible); err != nil {
		t.Fatalf("CreateSession(%s): %v", id, err)
	}
}

func rollbackBoolPtr(b bool) *bool { return &b }

// metaRollbackCase is one mutator under a failing write: setup builds a
// baseline with working writes, invoke runs the setter under a failing
// one, and the harness checks the error plus every session in ids.
type metaRollbackCase struct {
	method string
	setup  func(t *testing.T, s *Store)
	invoke func(s *Store) error
	ids    []string
}

var metaRollbackCases = []metaRollbackCase{
	{
		method: "SetAgent",
		setup: func(t *testing.T, s *Store) {
			mustCreateRollbackSession(t, s, "s1", "", "plan", true)
		},
		invoke: func(s *Store) error { _, err := s.SetAgent("s1", "build"); return err },
		ids:    []string{"s1"},
	},
	{
		method: "SetTitle",
		setup: func(t *testing.T, s *Store) {
			mustCreateRollbackSession(t, s, "s1", "", "general-purpose", true)
		},
		invoke: func(s *Store) error { _, err := s.SetTitle("s1", "new title"); return err },
		ids:    []string{"s1"},
	},
	{
		method: "SetWorkspace",
		setup: func(t *testing.T, s *Store) {
			mustCreateRollbackSession(t, s, "s1", "", "general-purpose", true)
		},
		invoke: func(s *Store) error { _, err := s.SetWorkspace("s1", "/elsewhere"); return err },
		ids:    []string{"s1"},
	},
	{
		method: "SetPermission",
		setup: func(t *testing.T, s *Store) {
			mustCreateRollbackSession(t, s, "s1", "", "general-purpose", true)
		},
		invoke: func(s *Store) error {
			_, err := s.SetPermission("s1", SwitchSkipTools, rollbackBoolPtr(true))
			return err
		},
		ids: []string{"s1"},
	},
	{
		method: "SetPermissionClear",
		setup: func(t *testing.T, s *Store) {
			mustCreateRollbackSession(t, s, "s1", "", "general-purpose", true)
			if _, err := s.SetPermission("s1", SwitchSkipAll, rollbackBoolPtr(true)); err != nil {
				t.Fatalf("baseline SetPermission: %v", err)
			}
		},
		invoke: func(s *Store) error {
			_, err := s.SetPermission("s1", SwitchSkipAll, nil)
			return err
		},
		ids: []string{"s1"},
	},
	{
		method: "SetEffortWide",
		setup: func(t *testing.T, s *Store) {
			mustCreateRollbackSession(t, s, "s1", "", "general-purpose", true)
			if _, err := s.SetEffort("s1", "", "high"); err != nil {
				t.Fatalf("baseline SetEffort: %v", err)
			}
		},
		invoke: func(s *Store) error { _, err := s.SetEffort("s1", "", "xhigh"); return err },
		ids:    []string{"s1"},
	},
	{
		method: "SetEffortModel",
		setup: func(t *testing.T, s *Store) {
			mustCreateRollbackSession(t, s, "s1", "", "general-purpose", true)
			if _, err := s.SetEffort("s1", "m1", "low"); err != nil {
				t.Fatalf("baseline SetEffort: %v", err)
			}
		},
		invoke: func(s *Store) error { _, err := s.SetEffort("s1", "m2", "high"); return err },
		ids:    []string{"s1"},
	},
	{
		method: "SetEffortClear",
		setup: func(t *testing.T, s *Store) {
			mustCreateRollbackSession(t, s, "s1", "", "general-purpose", true)
			if _, err := s.SetEffort("s1", "m1", "low"); err != nil {
				t.Fatalf("baseline SetEffort: %v", err)
			}
			if _, err := s.SetEffort("s1", "", "high"); err != nil {
				t.Fatalf("baseline SetEffort: %v", err)
			}
		},
		invoke: func(s *Store) error { _, err := s.SetEffort("s1", "m1", ""); return err },
		ids:    []string{"s1"},
	},
	{
		method: "SetMCPEnabledOff",
		setup: func(t *testing.T, s *Store) {
			mustCreateRollbackSession(t, s, "s1", "", "general-purpose", true)
		},
		invoke: func(s *Store) error { _, err := s.SetMCPEnabled("s1", "srv", false); return err },
		ids:    []string{"s1"},
	},
	{
		method: "SetMCPEnabledOn",
		setup: func(t *testing.T, s *Store) {
			mustCreateRollbackSession(t, s, "s1", "", "general-purpose", true)
			if _, err := s.SetMCPEnabled("s1", "srv", false); err != nil {
				t.Fatalf("baseline SetMCPEnabled: %v", err)
			}
		},
		invoke: func(s *Store) error { _, err := s.SetMCPEnabled("s1", "srv", true); return err },
		ids:    []string{"s1"},
	},
	{
		method: "SetSessionProfile",
		setup: func(t *testing.T, s *Store) {
			mustCreateRollbackSession(t, s, "s1", "", "general-purpose", true)
			if _, err := s.SetSessionProfile("s1", "a", "p1"); err != nil {
				t.Fatalf("baseline SetSessionProfile: %v", err)
			}
		},
		invoke: func(s *Store) error { _, err := s.SetSessionProfile("s1", "a", "p2"); return err },
		ids:    []string{"s1"},
	},
	{
		method: "SetSessionProfileClear",
		setup: func(t *testing.T, s *Store) {
			mustCreateRollbackSession(t, s, "s1", "", "general-purpose", true)
			if _, err := s.SetSessionProfile("s1", "a", "p1"); err != nil {
				t.Fatalf("baseline SetSessionProfile: %v", err)
			}
		},
		invoke: func(s *Store) error { _, err := s.SetSessionProfile("s1", "a", ""); return err },
		ids:    []string{"s1"},
	},
	{
		method: "Archive",
		setup: func(t *testing.T, s *Store) {
			mustCreateRollbackSession(t, s, "s1", "", "general-purpose", true)
		},
		invoke: func(s *Store) error { _, err := s.Archive("s1"); return err },
		ids:    []string{"s1"},
	},
	{
		method: "Retrieve",
		setup: func(t *testing.T, s *Store) {
			mustCreateRollbackSession(t, s, "s1", "", "general-purpose", true)
			if _, err := s.Archive("s1"); err != nil {
				t.Fatalf("baseline Archive: %v", err)
			}
		},
		invoke: func(s *Store) error { _, err := s.Retrieve("s1"); return err },
		ids:    []string{"s1"},
	},
	{
		method: "SetOrder",
		setup: func(t *testing.T, s *Store) {
			mustCreateRollbackSession(t, s, "s1", "", "general-purpose", true)
			mustCreateRollbackSession(t, s, "s2", "", "general-purpose", true)
			if err := s.SetOrder([]string{"s2", "s1"}); err != nil {
				t.Fatalf("baseline SetOrder: %v", err)
			}
		},
		invoke: func(s *Store) error { return s.SetOrder([]string{"s1", "s2"}) },
		ids:    []string{"s1", "s2"},
	},
	{
		method: "SetGroups",
		setup: func(t *testing.T, s *Store) {
			mustCreateRollbackSession(t, s, "s1", "", "general-purpose", true)
			if err := s.SetGroups([]string{"work"}, nil); err != nil {
				t.Fatalf("baseline SetGroups: %v", err)
			}
			if _, err := s.SetSessionGroup("s1", "work"); err != nil {
				t.Fatalf("baseline SetSessionGroup: %v", err)
			}
		},
		invoke: func(s *Store) error {
			return s.SetGroups([]string{"work-renamed"}, &GroupRename{From: "work", To: "work-renamed"})
		},
		ids: []string{"s1"},
	},
	{
		method: "SetSessionGroup",
		setup: func(t *testing.T, s *Store) {
			mustCreateRollbackSession(t, s, "s1", "", "general-purpose", true)
			if err := s.SetGroups([]string{"work"}, nil); err != nil {
				t.Fatalf("baseline SetGroups: %v", err)
			}
		},
		invoke: func(s *Store) error { _, err := s.SetSessionGroup("s1", "work"); return err },
		ids:    []string{"s1"},
	},
}

// A failed metadata write leaves the session exactly as it was.
//
// Each case builds a baseline with working writes, fails the next write,
// and then requires two things: the call reports the write error itself,
// and a Get over every touched session matches the record from before
// the call, field for field. The comparison is over the whole record,
// not just the field the setter names, because a rollback that restores
// one field while dropping a map entry is still a disagreement.
func TestAFailedMetaWriteLeavesTheSessionExactlyAsItWas(t *testing.T) {
	for _, tc := range metaRollbackCases {
		t.Run(tc.method, func(t *testing.T) {
			s, err := NewStore(t.TempDir())
			if err != nil {
				t.Fatalf("NewStore: %v", err)
			}
			t.Cleanup(s.Close)
			tc.setup(t, s)

			before := map[string]Session{}
			for _, id := range tc.ids {
				got, err := s.Get(id)
				if err != nil {
					t.Fatalf("Get(%s) before: %v", id, err)
				}
				before[id] = *got
			}

			failMetaWrites(s)
			// errors.Is, not ==: a method may wrap the write error to say
			// which record it was writing, which is the useful half when
			// one call writes several. What must not happen is the error
			// being swallowed or replaced.
			if err := tc.invoke(s); !errors.Is(err, errInjectedWrite) {
				t.Fatalf("%s under a failing write returned err = %v, want it to carry the write error", tc.method, err)
			}

			for _, id := range tc.ids {
				after, err := s.Get(id)
				if err != nil {
					t.Fatalf("Get(%s) after: %v", id, err)
				}
				if !reflect.DeepEqual(before[id], *after) {
					t.Errorf("%s left %s changed in memory: before = %+v, after = %+v", tc.method, id, before[id], *after)
				}
			}
		})
	}
}

// A write that fails partway through a reorder leaves every order as it
// was.
//
// SetOrder persists one file per session, so the failure can land after
// some sessions were already rewritten on disk. The store cannot take
// those bytes back, but it can refuse to claim them: every in-memory
// order goes back, so the daemon answers with the list it had.
func TestAFailedReorderLeavesEveryOrderAsItWas(t *testing.T) {
	s, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	t.Cleanup(s.Close)
	for _, id := range []string{"s1", "s2", "s3"} {
		mustCreateRollbackSession(t, s, id, "", "general-purpose", true)
	}
	if err := s.SetOrder([]string{"s3", "s2", "s1"}); err != nil {
		t.Fatalf("baseline SetOrder: %v", err)
	}

	before := map[string]Session{}
	for _, id := range []string{"s1", "s2", "s3"} {
		got, err := s.Get(id)
		if err != nil {
			t.Fatalf("Get(%s) before: %v", id, err)
		}
		before[id] = *got
	}

	calls := 0
	s.writeMeta = func(string, Session) error {
		calls++
		if calls >= 2 {
			return errInjectedWrite
		}
		return nil
	}
	if err := s.SetOrder([]string{"s1", "s2", "s3"}); err != errInjectedWrite {
		t.Fatalf("SetOrder under a failing write returned err = %v, want the write error itself", err)
	}

	for _, id := range []string{"s1", "s2", "s3"} {
		after, err := s.Get(id)
		if err != nil {
			t.Fatalf("Get(%s) after: %v", id, err)
		}
		if !reflect.DeepEqual(before[id], *after) {
			t.Errorf("failed SetOrder left %s changed in memory: before = %+v, after = %+v", id, before[id], *after)
		}
	}
}

// A retrieve whose renumber fails partway leaves the session archived
// and every order as it was.
//
// Retrieve restores the archived flag when renumberLocked fails, and the
// renumber restores the orders it touched before the failure, so the
// session is exactly as it was on both axes at once.
func TestAFailedRetrieveLeavesTheArchiveAndEveryOrderAsTheyWere(t *testing.T) {
	s, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	t.Cleanup(s.Close)
	for _, id := range []string{"s1", "s2", "s3"} {
		mustCreateRollbackSession(t, s, id, "", "general-purpose", true)
	}
	if _, err := s.Archive("s1"); err != nil {
		t.Fatalf("baseline Archive: %v", err)
	}
	if err := s.SetOrder([]string{"s3", "s2"}); err != nil {
		t.Fatalf("baseline SetOrder: %v", err)
	}

	before := map[string]Session{}
	for _, id := range []string{"s1", "s2", "s3"} {
		got, err := s.Get(id)
		if err != nil {
			t.Fatalf("Get(%s) before: %v", id, err)
		}
		before[id] = *got
	}
	if before["s1"].ArchivedAt == nil {
		t.Fatal("baseline s1 is not archived")
	}

	calls := 0
	s.writeMeta = func(string, Session) error {
		calls++
		if calls >= 2 {
			return errInjectedWrite
		}
		return nil
	}
	if _, err := s.Retrieve("s1"); err != errInjectedWrite {
		t.Fatalf("Retrieve under a failing write returned err = %v, want the write error itself", err)
	}

	for _, id := range []string{"s1", "s2", "s3"} {
		after, err := s.Get(id)
		if err != nil {
			t.Fatalf("Get(%s) after: %v", id, err)
		}
		if !reflect.DeepEqual(before[id], *after) {
			t.Errorf("failed Retrieve left %s changed in memory: before = %+v, after = %+v", id, before[id], *after)
		}
	}
	if after, _ := s.Get("s1"); after.ArchivedAt == nil {
		t.Error("failed Retrieve left s1 unarchived in memory while its file still says archived")
	}
}

// A successful write still sets the value, persists it, and survives a
// reload.
//
// The rollback must not change the success path: every setter below
// writes through to disk, and a store loaded fresh from that disk
// answers the same as the live one. The assertions read through the
// same accessors the daemon uses (Get, EffortFor, ProfileFor, MCPOffIn,
// the permission answer, the visible list), not through the fields the
// setters happen to assign.
func TestSuccessfulMetaWritesPersistAndSurviveAReload(t *testing.T) {
	dir := t.TempDir()
	s, err := NewStore(dir)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	t.Cleanup(s.Close)
	mustCreateRollbackSession(t, s, "s1", "", "plan", true)
	mustCreateRollbackSession(t, s, "s2", "", "plan", true)

	if _, err := s.SetAgent("s1", "build"); err != nil {
		t.Fatalf("SetAgent: %v", err)
	}
	if _, err := s.SetTitle("s1", "a title"); err != nil {
		t.Fatalf("SetTitle: %v", err)
	}
	if _, err := s.SetWorkspace("s1", "/work/here"); err != nil {
		t.Fatalf("SetWorkspace: %v", err)
	}
	if _, err := s.SetPermission("s1", SwitchSkipTools, rollbackBoolPtr(true)); err != nil {
		t.Fatalf("SetPermission: %v", err)
	}
	if _, err := s.SetEffort("s1", "", "high"); err != nil {
		t.Fatalf("SetEffort wide: %v", err)
	}
	if _, err := s.SetEffort("s1", "m1", "low"); err != nil {
		t.Fatalf("SetEffort model: %v", err)
	}
	if _, err := s.SetMCPEnabled("s1", "srv", false); err != nil {
		t.Fatalf("SetMCPEnabled: %v", err)
	}
	if _, err := s.SetSessionProfile("s1", "a", "p1"); err != nil {
		t.Fatalf("SetSessionProfile: %v", err)
	}
	if err := s.SetOrder([]string{"s2", "s1"}); err != nil {
		t.Fatalf("SetOrder: %v", err)
	}

	got, err := s.Get("s1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Agent != "build" {
		t.Errorf("Agent = %q, want %q", got.Agent, "build")
	}
	if got.Title != "a title" {
		t.Errorf("Title = %q, want %q", got.Title, "a title")
	}
	if got.Workspace != "/work/here" {
		t.Errorf("Workspace = %q, want %q", got.Workspace, "/work/here")
	}
	if ans := got.Permissions.Get(SwitchSkipTools); ans == nil || !*ans {
		t.Error("skip_tools answer was not kept")
	}
	if e := s.EffortFor("s1", "m1"); e != "low" {
		t.Errorf("EffortFor(m1) = %q, want %q", e, "low")
	}
	if e := s.EffortFor("s1", "other"); e != "high" {
		t.Errorf("EffortFor(other) = %q, want the conversation-wide %q", e, "high")
	}
	if off := s.MCPOffIn("s1"); !off["srv"] {
		t.Error("srv was not kept off")
	}
	if p := s.ProfileFor("s1", "a"); p != "p1" {
		t.Errorf("ProfileFor(a) = %q, want %q", p, "p1")
	}
	if visible := s.ListVisible(); len(visible) != 2 || visible[0].ID != "s2" || visible[1].ID != "s1" {
		t.Errorf("visible order = %v, want [s2 s1]", visible)
	}

	restored, warnings, err := LoadAllFromDisk(dir)
	if err != nil {
		t.Fatalf("LoadAllFromDisk: %v", err)
	}
	t.Cleanup(restored.Close)
	if len(warnings) != 0 {
		t.Errorf("warnings = %v, want none", warnings)
	}
	for _, id := range []string{"s1", "s2"} {
		want, err := s.Get(id)
		if err != nil {
			t.Fatalf("Get(%s): %v", id, err)
		}
		back, err := restored.Get(id)
		if err != nil {
			t.Fatalf("restored Get(%s): %v", id, err)
		}
		if !reflect.DeepEqual(*want, *back) {
			t.Errorf("restored %s = %+v, want %+v", id, *back, *want)
		}
	}
}

// Calls that persist nothing succeed when the disk would fail.
//
// Archiving an already-archived session and retrieving an active one
// are no-ops that never reach the write, and an unknown switch is
// rejected before it. None of them may fail just because the next real
// write would, and none may mutate anything.
func TestCallsThatPersistNothingIgnoreAFailingDisk(t *testing.T) {
	s, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	t.Cleanup(s.Close)
	mustCreateRollbackSession(t, s, "s1", "", "general-purpose", true)
	mustCreateRollbackSession(t, s, "s2", "", "general-purpose", true)
	if _, err := s.Archive("s1"); err != nil {
		t.Fatalf("baseline Archive: %v", err)
	}

	before1, _ := s.Get("s1")
	before2, _ := s.Get("s2")
	failMetaWrites(s)

	if _, err := s.Archive("s1"); err != nil {
		t.Errorf("re-archiving an archived session failed with a failing disk: %v", err)
	}
	if _, err := s.Retrieve("s2"); err != nil {
		t.Errorf("retrieving an active session failed with a failing disk: %v", err)
	}
	if _, err := s.SetPermission("s2", Switch("no-such-switch"), rollbackBoolPtr(true)); err == nil {
		t.Error("unknown switch was accepted with a failing disk")
	}

	for id, before := range map[string]*Session{"s1": before1, "s2": before2} {
		after, err := s.Get(id)
		if err != nil {
			t.Fatalf("Get(%s) after: %v", id, err)
		}
		if !reflect.DeepEqual(*before, *after) {
			t.Errorf("%s changed without a write: before = %+v, after = %+v", id, *before, *after)
		}
	}
}

// Every mutator that persists metadata is driven by the rollback table.
//
// A new setter with the mutate-then-persist shape and no test row fails
// here by name. The roster is built from the store's own method set, so
// adding a Set* method without a case in metaRollbackCases (or an
// Archive/Retrieve-shaped pair without coverage above) is a visible
// failure, not a silent gap. Coverage is by method name: variants that
// share one method (the effort branches, the permission clear) ride on
// their method's row.
func TestEveryMetaMutatorIsCoveredByTheRollbackRoster(t *testing.T) {
	covered := map[string]bool{}
	for _, tc := range metaRollbackCases {
		covered[tc.method] = true
	}
	storeMethods := reflect.TypeOf(&Store{})
	var missing []string
	for i := 0; i < storeMethods.NumMethod(); i++ {
		name := storeMethods.Method(i).Name
		if !strings.HasPrefix(name, "Set") {
			continue
		}
		found := false
		for tc := range covered {
			if tc == name || strings.HasPrefix(tc, name) {
				found = true
				break
			}
		}
		if !found {
			missing = append(missing, name)
		}
	}
	for _, name := range []string{"Archive", "Retrieve"} {
		if !covered[name] {
			missing = append(missing, name)
		}
	}
	if len(missing) != 0 {
		t.Errorf("persisting mutators without a rollback case: %v; add a row to metaRollbackCases", missing)
	}
}
