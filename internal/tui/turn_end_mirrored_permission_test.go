package tui

import "testing"

// dropHeldAsk lets go of the model question at a turn's end and not of a
// permission request, because one mirrored from a background task (or a
// scheduled run) into this conversation outlives the turn it is shown in.
//
// The order is the ordinary one: the parent's turn spawns a background
// task and carries on, the task asks for permission (mirrored here with
// task_session), and the parent's turn ends while the task still waits.
func TestATurnEndKeepsAMirroredPermissionRequest(t *testing.T) {
	for _, end := range []string{"turn.done", "turn.cancelled"} {
		t.Run(end, func(t *testing.T) {
			const (
				user    = `{"seq":10,"type":"message.user","data":{"text":"look into it in the background"}}`
				spawned = `{"seq":11,"type":"task.spawned","data":{"task_id":"t-1","agent":"explore","prompt":"list"}}`
				// As PermissionBroker writes a mirror: the description
				// prefixed, task_session naming the asker.
				mirrored = `{"seq":12,"type":"permission.request","data":{"id":"paaaaaa-1","tool":"bash","description":"[background task t-1] ls","rule":"ls *","can_always":false,"once":false,"task_session":"t-1"}}`
			)
			m := newTestModel()
			for _, f := range []string{user, spawned, mirrored} {
				upd, _ := m.handleServerEvent(eventMsg{gen: m.streamGen, ev: frame(t, f)})
				m = upd.(Model)
			}
			if m.pending == nil || m.pending.id != "paaaaaa-1" {
				t.Fatalf("precondition: the mirrored request is not on screen: %+v", m.pending)
			}
			done := `{"seq":13,"type":"` + end + `","data":{}}`
			upd, _ := m.handleServerEvent(eventMsg{gen: m.streamGen, ev: frame(t, done)})
			m = upd.(Model)
			if m.pending == nil || m.pending.id != "paaaaaa-1" {
				t.Errorf("the parent's %s took the background task's permission request off screen (pending=%+v); the task still waits on it", end, m.pending)
			}
		})
	}
}

// Every request queued behind it is kept as well: two background tasks
// asking at once, and the parent's turn ending before either is answered.
func TestATurnEndKeepsQueuedMirroredPermissionRequests(t *testing.T) {
	for _, end := range []string{"turn.done", "turn.cancelled"} {
		t.Run(end, func(t *testing.T) {
			frames := []string{
				`{"seq":10,"type":"message.user","data":{"text":"two in the background"}}`,
				`{"seq":11,"type":"task.spawned","data":{"task_id":"t-1","agent":"explore","prompt":"a"}}`,
				`{"seq":12,"type":"task.spawned","data":{"task_id":"t-2","agent":"explore","prompt":"b"}}`,
				`{"seq":13,"type":"permission.request","data":{"id":"paaaaaa-1","tool":"bash","description":"[background task t-1] ls","rule":"ls *","task_session":"t-1"}}`,
				`{"seq":14,"type":"permission.request","data":{"id":"paaaaaa-2","tool":"bash","description":"[background task t-2] pwd","rule":"pwd *","task_session":"t-2"}}`,
				`{"seq":15,"type":"` + end + `","data":{}}`,
				`{"seq":16,"type":"permission.resolved","data":{"id":"paaaaaa-1","allow":true,"scope":"once"}}`,
			}
			m := newTestModel()
			for _, f := range frames {
				upd, _ := m.handleServerEvent(eventMsg{gen: m.streamGen, ev: frame(t, f)})
				m = upd.(Model)
			}
			if m.pending == nil || m.pending.id != "paaaaaa-2" {
				t.Errorf("after the parent's %s and the first task's answer, the second task's request is not on screen (pending=%+v, queue=%d); that task still waits on it", end, m.pending, len(m.pendingQueue))
			}
		})
	}
}
