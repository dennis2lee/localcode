package daemon

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"localcode/internal/events"
	"localcode/internal/session"
	"localcode/internal/update"
)

// The failed-install line rides the event stream, not a page-load
// fetch: the stream replays the conversation asynchronously, and a fetch
// that resolved first drew the line above the replay, out of sight in
// any conversation longer than a screen. Written after the backlog, the
// line lands at the end of what the client has drawn.
//
// These tests open real streams over loopback, so they need a machine
// that allows it: they run in CI and on a developer machine, not in a
// sandbox that forbids binds. The one exception is the failing-write
// test, which calls maybeMSINotice directly and runs anywhere.

// noticeStreamDaemon restores the sessions in sessionDir, points the
// record lookup at recDir, and stamps the daemon with version.
func noticeStreamDaemon(t *testing.T, sessionDir, recDir, version string) *Daemon {
	t.Helper()
	store, warnings, err := session.LoadAllFromDisk(sessionDir)
	if err != nil {
		t.Fatalf("load sessions: %v", err)
	}
	t.Cleanup(store.Close)
	for _, w := range warnings {
		t.Logf("restore warning: %v", w)
	}
	d := newTestDaemon(t, "http://127.0.0.1:1")
	d.Loop.Store = store
	d.Version = version
	old := msiRecordDir
	msiRecordDir = func() (string, error) { return recDir, nil }
	t.Cleanup(func() { msiRecordDir = old })
	return d
}

func writeNoticeTestRecord(t *testing.T, recDir string, code int) {
	t.Helper()
	if err := update.WriteMSIRecord(recDir, update.MSIRecord{
		Version:  "0.46.0",
		ExitCode: code,
		Log:      filepath.Join(recDir, "localcode-0.46.0-msi.log"),
		Time:     time.Now().Truncate(time.Second),
	}); err != nil {
		t.Fatal(err)
	}
}

// shortHeartbeat makes the stream delimit what it wrote before blocking,
// the same trick attachAndRead uses: the notice is written synchronously
// after the backlog, so everything before the first heartbeat is the
// whole of what the open drew.
func shortHeartbeat(t *testing.T) {
	t.Helper()
	prev := heartbeatInterval
	heartbeatInterval = 30 * time.Millisecond
	t.Cleanup(func() { heartbeatInterval = prev })
}

// readStreamToHeartbeat opens the session's event stream and returns
// every line written before the first heartbeat comment.
func readStreamToHeartbeat(t *testing.T, url string) []string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("open stream: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("open stream = %d, want 200", resp.StatusCode)
	}
	var lines []string
	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)
	for scanner.Scan() {
		ln := scanner.Text()
		if strings.HasPrefix(ln, ":") {
			break
		}
		lines = append(lines, ln)
	}
	return lines
}

// ssePair is one event as written: the `id:` line that preceded its
// `data:` line, if any. A transient event has no `id:` line.
type ssePair struct {
	id   string
	data string
}

func pairStreamLines(lines []string) []ssePair {
	var pairs []ssePair
	pending := ""
	for _, ln := range lines {
		if rest, ok := strings.CutPrefix(ln, "id: "); ok {
			pending = rest
			continue
		}
		if rest, ok := strings.CutPrefix(ln, "data: "); ok {
			pairs = append(pairs, ssePair{id: pending, data: rest})
			pending = ""
		}
	}
	return pairs
}

func noticePairs(pairs []ssePair) []ssePair {
	var out []ssePair
	for _, p := range pairs {
		if strings.Contains(p.data, "did not install") {
			out = append(out, p)
		}
	}
	return out
}

// A stream opened on a conversation with a backlog gets the notice as
// the last event after the backlog, with no `id:` line, as a recovered
// error carrying the panel's line. It is never written to the session's
// log.
func TestTheStreamCarriesTheNoticeAfterTheBacklog(t *testing.T) {
	shortHeartbeat(t)
	sessionDir := t.TempDir()
	const id = "s-notice-backlog"
	writeSessionLog(t, sessionDir, id, []events.Event{
		logEvent(1, events.TypeUserMessage, "first question"),
		logEvent(2, events.TypeUserMessage, "second question"),
	})
	recDir := t.TempDir()
	writeNoticeTestRecord(t, recDir, 1625)
	d := noticeStreamDaemon(t, sessionDir, recDir, "0.45.2")
	srv := httptest.NewServer(d.Handler())
	defer srv.Close()

	pairs := pairStreamLines(readStreamToHeartbeat(t, srv.URL+"/api/sessions/"+id+"/events"))
	if len(pairs) != 3 {
		t.Fatalf("stream drew %d events, want 2 backlog plus the notice: %v", len(pairs), pairs)
	}
	if !strings.Contains(pairs[0].data, "first question") || pairs[0].id == "" {
		t.Errorf("first event = %+v, want the backlog with its resume id", pairs[0])
	}
	if !strings.Contains(pairs[1].data, "second question") || pairs[1].id == "" {
		t.Errorf("second event = %+v, want the backlog with its resume id", pairs[1])
	}
	got := pairs[2]
	if got.id != "" {
		t.Errorf("notice has id %q, want no `id:` line: it must not move the resume point", got.id)
	}
	var ev events.Event
	if err := json.Unmarshal([]byte(got.data), &ev); err != nil {
		t.Fatalf("notice is not an event: %v", err)
	}
	if ev.Type != events.TypeError {
		t.Errorf("notice type = %q, want error", ev.Type)
	}
	if ev.Seq != 0 {
		t.Errorf("notice seq = %d, want 0: a transient event carries no sequence", ev.Seq)
	}
	if recovered, _ := ev.Data["recovered"].(bool); !recovered {
		t.Errorf("notice data = %v, want recovered true: both clients draw it as a note that ends nothing", ev.Data)
	}
	rec, err := update.ReadMSIRecord(recDir)
	if err != nil {
		t.Fatal(err)
	}
	if line, _ := ev.Data["error"].(string); line != update.MSIFailureLine(rec) {
		t.Errorf("notice line = %q, want the Go-built panel line %q", line, update.MSIFailureLine(rec))
	}
	raw, err := os.ReadFile(filepath.Join(sessionDir, id+".jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "did not install") {
		t.Error("the notice was written to the session's log: the stream write is the whole of it")
	}
	if _, owed := update.MSINoticePending(recDir, "0.45.2"); owed {
		t.Error("the drawn notice is still owed: the successful write must mark it shown")
	}
}

// A second stream opened afterwards gets none: the line is drawn once.
func TestTheSecondStreamGetsNone(t *testing.T) {
	shortHeartbeat(t)
	sessionDir := t.TempDir()
	const id = "s-notice-second"
	writeSessionLog(t, sessionDir, id, []events.Event{
		logEvent(1, events.TypeUserMessage, "only question"),
	})
	recDir := t.TempDir()
	writeNoticeTestRecord(t, recDir, 1625)
	d := noticeStreamDaemon(t, sessionDir, recDir, "0.45.2")
	srv := httptest.NewServer(d.Handler())
	defer srv.Close()
	url := srv.URL + "/api/sessions/" + id + "/events"

	if first := noticePairs(pairStreamLines(readStreamToHeartbeat(t, url))); len(first) != 1 {
		t.Fatalf("first stream drew %d notices, want 1", len(first))
	}
	if second := noticePairs(pairStreamLines(readStreamToHeartbeat(t, url))); len(second) != 0 {
		t.Errorf("second stream drew %d notices, want none: the line is drawn once", len(second))
	}
}

// A cancelled record, an installed record, and a record for the running
// version send nothing on the stream.
func TestTheStreamIsSilentWithoutAFailure(t *testing.T) {
	shortHeartbeat(t)
	for _, tc := range []struct {
		name    string
		code    int
		running string
	}{
		{"cancelled", 1602, "0.45.2"},
		{"installed", 0, "0.45.2"},
		{"installed restart needed", 3010, "0.45.2"},
		{"installed restart started", 1641, "0.45.2"},
		{"running version", 1603, "0.46.0"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sessionDir := t.TempDir()
			const id = "s-notice-silent"
			writeSessionLog(t, sessionDir, id, []events.Event{
				logEvent(1, events.TypeUserMessage, "only question"),
			})
			recDir := t.TempDir()
			writeNoticeTestRecord(t, recDir, tc.code)
			d := noticeStreamDaemon(t, sessionDir, recDir, tc.running)
			srv := httptest.NewServer(d.Handler())
			defer srv.Close()

			pairs := pairStreamLines(readStreamToHeartbeat(t, srv.URL+"/api/sessions/"+id+"/events"))
			if len(pairs) != 1 {
				t.Fatalf("stream drew %d events, want only the backlog: %v", len(pairs), pairs)
			}
			if got := noticePairs(pairs); len(got) != 0 {
				t.Errorf("stream drew a notice for exit %d running %s, want none", tc.code, tc.running)
			}
		})
	}
}

// failWriter fails every write, like a stream whose client went away
// mid-open.
type failWriter struct{ err error }

func (w failWriter) Write(p []byte) (int, error) { return 0, w.err }

// A write that fails leaves the notice owed: the mark happens only
// after the write to the stream succeeded. No loopback here, so this
// runs anywhere.
func TestAFailingWriteLeavesTheNoticeOwed(t *testing.T) {
	sessionDir := t.TempDir()
	const id = "s-notice-failwrite"
	writeSessionLog(t, sessionDir, id, []events.Event{
		logEvent(1, events.TypeUserMessage, "only question"),
	})
	recDir := t.TempDir()
	writeNoticeTestRecord(t, recDir, 1625)
	d := noticeStreamDaemon(t, sessionDir, recDir, "0.45.2")

	d.maybeMSINotice(failWriter{err: errors.New("client went away")}, func() {})
	if _, owed := update.MSINoticePending(recDir, "0.45.2"); !owed {
		t.Error("the notice is no longer owed after a failed write: it must stay owed for the next stream")
	}
	if _, err := os.Stat(update.MSINotifiedPath(recDir)); !os.IsNotExist(err) {
		t.Errorf("marker exists after a failed write: nothing was drawn, so nothing counts as shown")
	}
}

// Two streams opened at once draw the notice exactly once between them:
// the check, the write and the mark are one locked section. Run under
// -race: without the lock both would read owed before either marked.
func TestConcurrentStreamsDrawTheNoticeExactlyOnce(t *testing.T) {
	shortHeartbeat(t)
	sessionDir := t.TempDir()
	const id = "s-notice-race"
	writeSessionLog(t, sessionDir, id, []events.Event{
		logEvent(1, events.TypeUserMessage, "only question"),
	})
	recDir := t.TempDir()
	writeNoticeTestRecord(t, recDir, 1625)
	d := noticeStreamDaemon(t, sessionDir, recDir, "0.45.2")
	srv := httptest.NewServer(d.Handler())
	defer srv.Close()
	url := srv.URL + "/api/sessions/" + id + "/events"

	start := make(chan struct{})
	var wg sync.WaitGroup
	counts := make([]int, 2)
	for i := range counts {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			counts[i] = len(noticePairs(pairStreamLines(readStreamToHeartbeat(t, url))))
		}(i)
	}
	close(start)
	wg.Wait()
	if total := counts[0] + counts[1]; total != 1 {
		t.Errorf("concurrent streams drew %d notices (%v), want exactly 1 between them", total, counts)
	}
	if _, owed := update.MSINoticePending(recDir, "0.45.2"); owed {
		t.Error("the notice is still owed after two streams drew it once")
	}
}
