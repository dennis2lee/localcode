package tools

import (
	"context"
	"strings"
	"testing"
)

// An unrecognised decision from the resolver must refuse the call, not run
// it. The registry used to have no default in this switch, so a typo'd
// rule ("denied") fell through to execution with no prompt.
func TestRegistryCallUnknownDecisionRefusesWithoutExecuting(t *testing.T) {
	for _, bad := range []string{"denied", "DENY", "deny ", "block", ""} {
		r := NewRegistry(func(context.Context, Ask) (bool, error) { return true, nil })
		r.Resolver = func(context.Context, Query) Outcome { return Outcome{Decision: Decision(bad)} }

		ft := &fakeTool{name: "bash", needsPerm: true}
		r.Register(ft)

		result := r.Call(context.Background(), "bash", nil, "")
		if !result.IsError {
			t.Errorf("decision %q: expected an error, got success", bad)
		}
		if !result.Refused {
			t.Errorf("decision %q: expected Refused, so the loop stops rather than retries", bad)
		}
		if ft.executed {
			t.Errorf("decision %q: tool executed on an unknown decision", bad)
		}
		if !strings.Contains(result.Content, bad) {
			t.Errorf("decision %q: refusal %q does not name the bad value", bad, result.Content)
		}
	}
}

// The three known decisions keep their meaning through the same switch:
// allow runs, ask consults the permission handler, deny refuses.
func TestRegistryCallKnownDecisionsUnchanged(t *testing.T) {
	r := NewRegistry(func(context.Context, Ask) (bool, error) { return true, nil })

	ft := &fakeTool{name: "bash", needsPerm: true}
	r.Register(ft)

	for _, d := range []Decision{DecisionAllow, DecisionAsk, DecisionDeny} {
		r.Resolver = func(context.Context, Query) Outcome { return Outcome{Decision: d} }
		ft.executed = false
		result := r.Call(context.Background(), "bash", nil, "")
		switch d {
		case DecisionAllow, DecisionAsk:
			if result.IsError {
				t.Errorf("decision %q: unexpected error: %s", d, result.Content)
			}
			if !ft.executed {
				t.Errorf("decision %q: expected the tool to execute", d)
			}
			if result.Refused {
				t.Errorf("decision %q: must not be marked Refused", d)
			}
		case DecisionDeny:
			if !result.IsError || !result.Refused {
				t.Errorf("decision %q: expected a refused error, got %+v", d, result)
			}
			if ft.executed {
				t.Errorf("decision %q: tool must not execute", d)
			}
		}
	}
}

// Unknown stays refused even when the permission handler would approve
// anything: the refusal happens in the switch, before any prompt.
func TestRegistryCallUnknownDecisionNeverAsks(t *testing.T) {
	permCalled := false
	r := NewRegistry(func(context.Context, Ask) (bool, error) {
		permCalled = true
		return true, nil
	})
	r.Resolver = func(context.Context, Query) Outcome { return Outcome{Decision: "denied"} }

	ft := &fakeTool{name: "bash", needsPerm: true}
	r.Register(ft)

	result := r.Call(context.Background(), "bash", nil, "")
	if !result.Refused || ft.executed || permCalled {
		t.Errorf("unknown decision: Refused=%v executed=%v permCalled=%v, want true/false/false: %+v",
			result.Refused, ft.executed, permCalled, result)
	}
}
