package agent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"localcode/internal/events"
)

// What a booking tells the person, and what the list says about who made it.
//
// Two small things surfaced while the author of a booked prompt was being
// recorded. The reply to "/schedule" told every booking it would run under
// the conversation's permission settings, which is false of a "!" line: the
// shell escape never asks a rule, and the person who typed it is the one
// who is meant to know. And "/show-scheduled-task" could not say which
// bookings the model made, though the person is told to delete the ones
// they did not book themselves.

// scheduleReply books a "/schedule in 1 hour <prompt>" line and returns what
// the conversation was answered with.
func scheduleReply(t *testing.T, rig *authorRig, ctx context.Context, prompt string) string {
	t.Helper()
	line := "/schedule in 1 hour " + prompt
	if err := rig.loop.SendMessage(ctx, rig.sid, "general-purpose", line); err != nil {
		t.Fatalf("SendMessage(%q): %v", line, err)
	}
	return lastAssistantText(rig.loop.Store, rig.sid)
}

func TestTheScheduleReplyDoesNotPromisePermissionSettingsToAShellEscape(t *testing.T) {
	const (
		shellNote  = "runs as a shell command"
		noRule     = "no permission rule is consulted"
		permission = "permission settings"
		asText     = "plain text"
	)
	for _, tc := range []struct {
		name   string
		prompt string
		model  bool // the Command tool ran the line
		// what the reply has to say, and what it must not
		shell, permissions, text bool
	}{
		{"a shell escape a person books", "!echo hi", false, true, false, false},
		// "!!" is ordinary text, which reaches a model under the settings.
		{"the literal-text escape", "!!echo hi", false, false, true, false},
		{"ordinary text", "run the tests", false, false, true, false},
		// A slash command is not a shell escape and asks no more than it did.
		{"a slash command", "/compact", false, false, true, false},
		// What the model books is text whatever it starts with, so the
		// permission sentence is true, and the plain-text note is added.
		{"a shell escape the model books", "!echo hi", true, false, true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rig := newAuthorRig(t)
			ctx := context.Background()
			if tc.model {
				ctx = withCommandRun(ctx)
			}
			reply := scheduleReply(t, rig, ctx, tc.prompt)

			if got := strings.Contains(reply, shellNote) && strings.Contains(reply, noRule); got != tc.shell {
				t.Errorf("the reply says it runs as a shell command with no rule = %v, want %v:\n%s", got, tc.shell, reply)
			}
			if tc.shell && !strings.Contains(reply, "!!") {
				t.Errorf("the reply does not say how to book the text instead:\n%s", reply)
			}
			if got := strings.Contains(reply, permission); got != tc.permissions {
				t.Errorf("the reply promises the permission settings = %v, want %v:\n%s", got, tc.permissions, reply)
			}
			if got := strings.Contains(reply, asText); got != tc.text {
				t.Errorf("the reply says the prompt is plain text = %v, want %v:\n%s", got, tc.text, reply)
			}
			// And the booking itself is not changed by what it was told.
			if list := rig.sched.List(rig.sid); len(list) != 1 || list[0].Prompt != tc.prompt {
				t.Errorf("booked %+v, want the prompt %q once", list, tc.prompt)
			}
		})
	}
}

func TestShowScheduledTaskSaysWhichBookingsTheModelMade(t *testing.T) {
	rig := newAuthorRig(t)
	ctx := WithSessionID(context.Background(), rig.sid)

	// The two doors a booking is made through in this process.
	toolInput, _ := json.Marshal(map[string]string{"when": "in 1 hour", "prompt": "from the tool"})
	if res := NewScheduleTool(rig.loop).Execute(ctx, toolInput); res.IsError {
		t.Fatalf("Schedule: %s", res.Content)
	}
	if _, err := rig.sched.Add(rig.sid, "general-purpose", "from Add", time.Now().Add(time.Hour), RepeatOptions{}); err != nil {
		t.Fatalf("Add: %v", err)
	}
	// And what a restart finds: each author word, written the way the log
	// holds it. A booking from before the field has none, and one from a
	// later version may carry a word this version cannot read.
	want := map[string]bool{"s1": true, "s2": false}
	for id, by := range map[string]string{"s11": "model", "s12": "person", "s13": "", "s14": "a-later-kind"} {
		created := map[string]any{
			"id": id, "at": time.Now().Add(time.Hour).Format(time.RFC3339),
			"prompt": "restored " + id, "agent": "general-purpose",
		}
		if by != "" {
			created["by"] = by
		}
		if _, err := rig.loop.Store.Append(rig.sid, events.TypeScheduleCreated, created); err != nil {
			t.Fatalf("append %s: %v", id, err)
		}
		want[id] = by == "model"
	}
	rig.sched.Restore([]string{rig.sid}, time.Now())

	if err := rig.loop.SendMessage(context.Background(), rig.sid, "general-purpose", "/show-scheduled-task"); err != nil {
		t.Fatalf("SendMessage: %v", err)
	}
	reply := lastAssistantText(rig.loop.Store, rig.sid)

	seen := map[string]bool{}
	for _, row := range strings.Split(reply, "\n") {
		fields := strings.Fields(row)
		if len(fields) == 0 || !strings.HasPrefix(row, "s") || len(fields) < 3 {
			continue
		}
		id := fields[0]
		if _, listed := want[id]; !listed {
			continue
		}
		seen[id] = true
		if got := strings.Contains(row, "(booked by the model)"); got != want[id] {
			t.Errorf("%s is marked as the model's = %v, want %v:\n%s", id, got, want[id], row)
		}
	}
	for id := range want {
		if !seen[id] {
			t.Errorf("%s is not in the list at all:\n%s", id, reply)
		}
	}
}
