package agent

import (
	"testing"

	"localcode/internal/config"
	"localcode/internal/session"
)

// Every switch in Switches reaches a dedicated config accessor in byDefault.
//
// When a session has no answer of its own for a permission switch, it falls
// back to the daemon default configured in config.json. That fallback is
// resolved by PermissionPolicy.byDefault via an unexported switch statement.
//
// An unhandled switch in byDefault falls off the end and returns false. In an
// unconfigured daemon, every switch also defaults to false. A test asserting
// only that byDefault returns false cannot tell whether the switch reached a
// real accessor that returned false or hit the missing-case fallback.
//
// To distinguish the two, this test sets each switch's backing config field to
// true and verifies that byDefault turns true. It also enables all sibling
// switches while leaving the target switch disabled, confirming that each
// switch is bound to its own accessor rather than a shared or blanket flag.
func TestEveryPermissionSwitchReachesItsConfigAccessorByDefault(t *testing.T) {
	type switchBinding struct {
		name     string
		enable   func(c *config.Config)
		accessor func(c *config.Config) bool
	}

	bindings := map[session.Switch]switchBinding{
		session.SwitchSkipAll: {
			name:     "SkipPermissions",
			enable:   func(c *config.Config) { v := true; c.SkipPermissions = &v },
			accessor: func(c *config.Config) bool { return c.PermissionsSkipped() },
		},
		session.SwitchSkipTools: {
			name:     "SkipToolPermissions",
			enable:   func(c *config.Config) { v := true; c.SkipToolPermissions = &v },
			accessor: func(c *config.Config) bool { return c.ToolPermissionsSkipped() },
		},
		session.SwitchReadOutside: {
			name:     "ReadOutsideWorkspace",
			enable:   func(c *config.Config) { v := true; c.ReadOutsideWorkspace = &v },
			accessor: func(c *config.Config) bool { return c.ReadOutsideAllowed() },
		},
		session.SwitchWriteOutside: {
			name:     "WriteOutsideWorkspace",
			enable:   func(c *config.Config) { v := true; c.WriteOutsideWorkspace = &v },
			accessor: func(c *config.Config) bool { return c.WriteOutsideAllowed() },
		},
	}

	switches := session.Switches()
	if len(switches) == 0 {
		t.Fatal("session.Switches() returned an empty roster")
	}

	// Guard against drift between the roster and this test's bindings table.
	// If a switch is added to session.Switches() without adding a binding here,
	// the test fails immediately rather than silently skipping the new switch.
	for _, sw := range switches {
		if _, ok := bindings[sw]; !ok {
			t.Fatalf("switch %q in session.Switches() has no config binding in this test", sw)
		}
	}
	if len(bindings) != len(switches) {
		t.Fatalf("bindings table has %d entries, session.Switches() has %d", len(bindings), len(switches))
	}

	// A negative control, checked first. An unrecognized switch must return false
	// even when every known config accessor is set to true.
	allOnCfg := &config.Config{}
	for _, b := range bindings {
		b.enable(allOnCfg)
	}
	policyAllOn := NewPermissionPolicy(nil, allOnCfg)
	bogus := session.Switch("not-a-real-switch-negative-control")
	if policyAllOn.byDefault(bogus) {
		t.Fatalf("byDefault(%q) = true for an unhandled switch, want false", bogus)
	}

	// When policy has no config pointer, byDefault must safely return false.
	policyNil := NewPermissionPolicy(nil, nil)
	for _, sw := range switches {
		if policyNil.byDefault(sw) {
			t.Errorf("byDefault(%q) = true when policy.cfg is nil, want false", sw)
		}
	}

	for _, sw := range switches {
		binding := bindings[sw]
		t.Run(string(sw), func(t *testing.T) {
			// 1. Unset config default: accessor must report false, and byDefault must report false.
			emptyCfg := &config.Config{}
			if binding.accessor(emptyCfg) {
				t.Fatalf("accessor for %q returned true on empty config", binding.name)
			}
			policyEmpty := NewPermissionPolicy(nil, emptyCfg)
			if policyEmpty.byDefault(sw) {
				t.Fatalf("byDefault(%q) = true on empty config, want false", sw)
			}

			// 2. Accessor active: byDefault must return true.
			// This distinguishes "config default is false" from "switch has no case".
			// If byDefault is missing case sw, it silently returns false.
			activeCfg := &config.Config{}
			binding.enable(activeCfg)
			if !binding.accessor(activeCfg) {
				t.Fatalf("accessor for %q returned false after enable", binding.name)
			}
			policyActive := NewPermissionPolicy(nil, activeCfg)
			if !policyActive.byDefault(sw) {
				t.Fatalf("byDefault(%q) = false when its config accessor %s is true; switch is missing from byDefault", sw, binding.name)
			}

			// Verify that Effective reports the daemon default as the source.
			if val, src := policyActive.Effective("session-without-own-setting", sw); !val || src != SourceDefault {
				t.Errorf("Effective(%q) = (%v, %q), want (true, %q)", sw, val, src, SourceDefault)
			}

			// 3. Isolation: enable every switch EXCEPT sw.
			// byDefault(sw) must remain false. If sw is mapped to the wrong accessor
			// or byDefault uses a shared or blanket condition, this check fails.
			otherCfg := &config.Config{}
			for otherSw, otherBinding := range bindings {
				if otherSw != sw {
					otherBinding.enable(otherCfg)
				}
			}
			policyOther := NewPermissionPolicy(nil, otherCfg)
			if policyOther.byDefault(sw) {
				t.Fatalf("byDefault(%q) = true when other config accessors are true; switch is reading another switch's accessor", sw)
			}
		})
	}
}
