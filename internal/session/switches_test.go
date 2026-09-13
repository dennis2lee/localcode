package session

import "testing"

// Every switch in Switches round-trips through Permissions.set and Get.
//
// The switch roster in Switches() is the client-facing list of toggleable
// permissions. Storing an answer for a session requires two per-switch
// statements: Permissions.set to record the pointer (or nil to clear it),
// and Permissions.Get to read it back.
//
// If a switch is omitted from set, the failure is loud: set returns false,
// which Store.SetPermission converts into an error that surfaces in the
// API response. But an omission from Get fails completely silently:
// Get returns nil as its default fallback, so the session appears to have
// no setting of its own and the daemon default is reported as the effective
// source. The client can write the switch without error, but can never read
// it back.
//
// Walking Switches() ensures that no entry can be added to the roster
// without both set and Get handling it for true, false, and clearing back
// to nil, and that setting one switch does not mutate any sibling switch.
func TestEverySwitchInRosterRoundTripsThroughSetAndGet(t *testing.T) {
	switches := Switches()
	if len(switches) == 0 {
		t.Fatal("Switches() returned an empty roster")
	}

	trueVal := true
	falseVal := false

	// A negative control, checked first. If set reported true or Get reported
	// a pointer for an unknown switch, round-tripping would prove nothing.
	bogus := Switch("not-a-real-switch-negative-control")
	var control Permissions
	if control.set(bogus, &trueVal) {
		t.Errorf("set(%q) reported true for an unknown switch; set must reject unregistered switches", bogus)
	}
	if got := control.Get(bogus); got != nil {
		t.Errorf("Get(%q) = %v for an unknown switch, want nil", bogus, *got)
	}

	for _, sw := range switches {
		t.Run(string(sw), func(t *testing.T) {
			var p Permissions

			// Unset baseline: an untouched switch must return nil.
			if got := p.Get(sw); got != nil {
				t.Fatalf("Permissions{}.Get(%q) = %v, want nil", sw, *got)
			}

			// Setting true must report ok and read back true.
			if !p.set(sw, &trueVal) {
				t.Fatalf("set(%q, true) returned false; switch is missing from set", sw)
			}
			got := p.Get(sw)
			if got == nil {
				t.Fatalf("Get(%q) after set true returned nil; switch is missing from Get", sw)
			}
			if !*got {
				t.Errorf("Get(%q) = false, want true", sw)
			}

			// Setting false must report ok and read back false.
			if !p.set(sw, &falseVal) {
				t.Fatalf("set(%q, false) returned false; switch is missing from set", sw)
			}
			got = p.Get(sw)
			if got == nil {
				t.Fatalf("Get(%q) after set false returned nil; switch is missing from Get", sw)
			}
			if *got {
				t.Errorf("Get(%q) = true, want false", sw)
			}

			// Clearing back to nil must report ok and read back nil.
			if !p.set(sw, nil) {
				t.Fatalf("set(%q, nil) returned false; switch is missing from set", sw)
			}
			if got := p.Get(sw); got != nil {
				t.Errorf("Get(%q) after set nil returned %v, want nil", sw, *got)
			}
		})
	}
}

// Setting one switch does not mutate or clear any sibling switch in the roster.
//
// A copy-paste error in set (pointing at the wrong struct field) or in Get
// (reading the wrong struct field) could allow a switch to round-trip in
// isolation while corrupting or reading a sibling switch. Setting all switches
// and toggling one at a time ensures every switch in the roster addresses its
// own independent field.
func TestEverySwitchInRosterOperatesIndependently(t *testing.T) {
	switches := Switches()
	trueVal := true
	falseVal := false

	var p Permissions
	for _, sw := range switches {
		if !p.set(sw, &trueVal) {
			t.Fatalf("set(%q, true) returned false", sw)
		}
	}

	for _, target := range switches {
		if !p.set(target, &falseVal) {
			t.Fatalf("set(%q, false) returned false", target)
		}
		for _, sw := range switches {
			got := p.Get(sw)
			if got == nil {
				t.Fatalf("Get(%q) returned nil while checking independence of %q", sw, target)
			}
			if sw == target {
				if *got != false {
					t.Errorf("Get(%q) = %v, want false for target switch", sw, *got)
				}
			} else {
				if *got != true {
					t.Errorf("Get(%q) = %v, want true while toggling %q", sw, *got, target)
				}
			}
		}
		// Restore to true before testing the next switch.
		if !p.set(target, &trueVal) {
			t.Fatalf("set(%q, true) returned false during restore", target)
		}
	}
}
