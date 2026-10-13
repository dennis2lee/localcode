package daemon

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	"localcode/internal/events"
)

// Removing background tasks from a conversation's task panel.
//
// The panel listed every task a conversation had ever started, and the
// only way to remove one was to open it, wait for it to finish, and press
// Delete in its window. A conversation that delegated a lot grew a column
// of finished rows nobody wanted. These two routes remove one row, or all
// of them, from the panel itself.
//
// A row is not only a session. The panel is built from the parent's own
// log (task.spawned and task.status), so a row can outlive the session it
// names: a task whose conversation was deleted before the removal was
// recorded on the parent, or one removed by hand from the sessions
// directory. Deleting by session id alone could never remove such a row,
// because there is no session to delete. So both routes work from the
// rows, delete the sessions that still exist, and record the removal of
// every row either way.

// handleDeleteTask removes one row from a conversation's task panel,
// stopping and deleting the task's conversation and everything under it.
// A task still running is stopped first.
func (d *Daemon) handleDeleteTask(w http.ResponseWriter, r *http.Request) {
	parentID, taskID := r.PathValue("id"), r.PathValue("taskId")
	if _, err := d.Loop.Store.Get(parentID); err != nil {
		writeError(w, http.StatusNotFound, err)
		return
	}
	if !d.isTaskOf(parentID, taskID) {
		writeError(w, http.StatusNotFound, fmt.Errorf("%s is not a task of session %s", taskID, parentID))
		return
	}
	if status, err := d.deleteTasks(parentID, []string{taskID}); err != nil {
		writeError(w, status, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleDeleteTasks removes every row from a conversation's task panel,
// running tasks included. The conversation itself stays.
func (d *Daemon) handleDeleteTasks(w http.ResponseWriter, r *http.Request) {
	parentID := r.PathValue("id")
	if _, err := d.Loop.Store.Get(parentID); err != nil {
		writeError(w, http.StatusNotFound, err)
		return
	}
	rows, err := d.taskRowsOf(parentID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	if status, err := d.deleteTasks(parentID, rows); err != nil {
		writeError(w, status, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]int{"deleted": len(rows)})
}

// isTaskOf reports whether taskID is a row of parentID's task panel.
func (d *Daemon) isTaskOf(parentID, taskID string) bool {
	rows, err := d.taskRowsOf(parentID)
	if err != nil {
		return false
	}
	for _, id := range rows {
		if id == taskID {
			return true
		}
	}
	return false
}

// taskRowsOf is every row of parentID's task panel.
//
// From the log alone, not from the sessions parentID is the parent of: a
// scheduled run is a child session too, with no row here and a row of its
// own in the scheduled tasks panel, and deleting it from this one would
// leave that row pointing at a transcript that is gone.
func (d *Daemon) taskRowsOf(parentID string) ([]string, error) {
	evs, err := d.Loop.Store.Events(parentID, 0)
	if err != nil {
		return nil, err
	}
	return taskRows(evs), nil
}

// taskRows is the rows a session's task panel shows, in the order they
// first appeared, worked out from its log the way the clients work them
// out (events.js, internal/tui): task.spawned and task.status each name a
// row, a "deleted" status removes it, a status after that brings it back,
// and an orchestration stage is a progress line rather than a row.
func taskRows(evs []events.Event) []string {
	var order []string
	present := map[string]bool{}
	for _, ev := range evs {
		if ev.Type != events.TypeTaskSpawned && ev.Type != events.TypeTaskStatus {
			continue
		}
		id, _ := ev.Data["task_id"].(string)
		if id == "" || strings.HasPrefix(id, "orchestrate:") {
			continue
		}
		if status, _ := ev.Data["status"].(string); ev.Type == events.TypeTaskStatus && status == "deleted" {
			present[id] = false
			continue
		}
		if !present[id] {
			// Back at the end, as a row a client deleted and then saw
			// again is.
			order = removeID(order, id)
			order = append(order, id)
			present[id] = true
		}
	}
	out := order[:0]
	for _, id := range order {
		if present[id] {
			out = append(out, id)
		}
	}
	return out
}

func removeID(ids []string, id string) []string {
	for i, v := range ids {
		if v == id {
			return append(ids[:i], ids[i+1:]...)
		}
	}
	return ids
}

// deleteTasks stops and deletes the tasks of parentID named by rows, each
// with everything under it, and records on parentID's log that each row is
// gone. It returns the status to answer with when it fails.
//
// A row whose session no longer exists gets the record alone, and so does
// one whose session some other conversation started: the row is this
// panel's to remove, and the session is not.
//
// The same steps as deleting one conversation (handleDeleteSession), for
// several at once: hold each task's turn slot, claim and stop every tree,
// delete, record, and release the claims last.
func (d *Daemon) deleteTasks(parentID string, rows []string) (int, error) {
	if len(rows) == 0 {
		return 0, nil
	}
	var live []string
	for _, id := range rows {
		if sess, err := d.Loop.Store.Get(id); err == nil && sess.ParentID == parentID {
			live = append(live, id)
		}
	}

	// A turn somebody started in a task's conversation directly is
	// writing to it. All or nothing: a delete-all that removed half the
	// rows and refused the rest would leave the panel describing neither.
	// Exclusive, as archiving takes it: a message arriving for a task
	// being deleted is refused rather than queued for a turn that will
	// never run.
	_, cancel := context.WithCancel(context.Background())
	defer cancel()
	var held []string
	defer func() {
		for _, id := range held {
			d.turns.end(id)
		}
	}()
	for _, id := range live {
		if !d.turns.beginExclusive(id, cancel) {
			return http.StatusConflict, fmt.Errorf("task %s has a turn in progress", id)
		}
		held = append(held, id)
	}

	ids, release := d.Loop.StopSessionTrees(live)
	defer release()

	// Every row is recorded as gone except one whose session could not be
	// deleted, which stays on the panel to be tried again.
	failed := map[string]error{}
	for _, id := range live {
		// Gone already is gone: another delete got there first.
		if err := d.Loop.Store.DeleteTree(id); err != nil && !strings.Contains(err.Error(), "not found") {
			failed[id] = err
		}
	}
	// Recorded where the rows come from, so they stay gone across a
	// reload and reach every client watching. See handleDeleteSession.
	for _, id := range rows {
		if failed[id] != nil {
			continue
		}
		d.Loop.Store.Append(parentID, events.TypeTaskStatus, map[string]any{
			"task_id": id,
			"status":  "deleted",
		})
	}
	// What is gone, which is every claimed session unless a delete failed
	// partway through its tree.
	var gone []string
	for _, id := range ids {
		if _, err := d.Loop.Store.Get(id); err != nil {
			gone = append(gone, id)
		}
	}
	d.forgetDeleted(gone)
	for id, err := range failed {
		return http.StatusInternalServerError, fmt.Errorf("delete task %s: %w", id, err)
	}
	return 0, nil
}
