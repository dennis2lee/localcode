package daemon

import (
	"fmt"
	"net/http"
	"reflect"
	"sync"
	"testing"
	"time"

	"localcode/internal/events"
)

// The daemon-wide settings are numbered. A page hears of a change twice,
// by the answer to its request and by settings.changed on the broadcast,
// and nothing ordered the two: see settingsChanged. Each announcement
// carries the next number, the answer to the change that caused it
// carries the same one, and GET /api/settings carries the last.

// settingWriters is every route that changes a daemon-wide setting, with a
// body that changes it, and the field of the answer that says the value.
var settingWriters = []struct{ path, body, field string }{
	{"/api/settings/auto-delegate", `{"enabled":true}`, "auto_delegate"},
	{"/api/settings/smart-agent", `{"enabled":true}`, "smart_agent"},
	{"/api/settings/model-invocable", `{"enabled":true}`, "model_invocable"},
	{"/api/settings/orchestrate", `{"enabled":true}`, "orchestrate"},
	{"/api/settings/keep-going", `{"enabled":true}`, "keep_going"},
	{"/api/settings/fold-thinking", `{"enabled":true}`, "fold_thinking"},
	{"/api/settings/repeat-limit", `{"limit":5}`, "repeat_limit"},
	{"/api/permissions/skip", `{"enabled":true}`, "skip_permissions"},
	{"/api/settings/browser", `{"enabled":true}`, "browser"},
	{"/api/settings/ssh", `{"enabled":true}`, "ssh"},
	{"/api/settings/powershell", `{"enabled":true}`, "powershell"},
}

// announcements collects every settings.changed the daemon sends until
// stop is called, in the order they arrive.
func announcements(t *testing.T, d *Daemon) (stop func() []events.Event) {
	t.Helper()
	ch, unsub := d.daemonEvents.subscribe()
	var (
		mu   sync.Mutex
		got  []events.Event
		done = make(chan struct{})
		quit = make(chan struct{})
	)
	go func() {
		defer close(done)
		for {
			select {
			case ev := <-ch:
				if ev.Type == events.TypeSettingsChanged {
					mu.Lock()
					got = append(got, ev)
					mu.Unlock()
				}
			case <-quit:
				return
			}
		}
	}()
	return func() []events.Event {
		// What was sent before stop is in the channel already: drain it.
		time.Sleep(50 * time.Millisecond)
		close(quit)
		<-done
		unsub()
		mu.Lock()
		defer mu.Unlock()
		return got
	}
}

func settingNumber(t *testing.T, v any) uint64 {
	t.Helper()
	switch n := v.(type) {
	case uint64:
		return n
	case float64:
		return uint64(n)
	}
	t.Fatalf("seq is %T %v, want a number", v, v)
	return 0
}

// Every writer answers with the number of the announcement it caused, and
// the two say the same value.
func TestEverySettingChangeAnswersWithTheNumberItWasAnnouncedUnder(t *testing.T) {
	d, srv, _ := settingsDaemon(t, `{}`)
	stop := announcements(t, d)
	answers := map[uint64]map[string]any{}
	fields := map[uint64]string{}
	for _, w := range settingWriters {
		status, answered := postSetting(t, srv.URL+w.path, w.body)
		if status != http.StatusOK {
			t.Fatalf("POST %s answered %d %v", w.path, status, answered)
		}
		if _, ok := answered["seq"]; !ok {
			t.Fatalf("POST %s answered %v, with no number", w.path, answered)
		}
		n := settingNumber(t, answered["seq"])
		if _, dup := answers[n]; dup {
			t.Fatalf("POST %s answered number %d, which an earlier change was given", w.path, n)
		}
		answers[n], fields[n] = answered, w.field
	}
	got := stop()
	if len(got) != len(settingWriters) {
		t.Fatalf("%d announcements for %d changes", len(got), len(settingWriters))
	}
	var last uint64
	for _, ev := range got {
		n := settingNumber(t, ev.Data["seq"])
		if n <= last {
			t.Errorf("announcement %d arrived after %d: the numbers are not in the order they are sent", n, last)
		}
		last = n
		answered, ok := answers[n]
		if !ok {
			t.Errorf("announcement %d has no answer with its number", n)
			continue
		}
		f := fields[n]
		if fmt.Sprint(answered[f]) != fmt.Sprint(ev.Data[f]) {
			t.Errorf("number %d: the answer says %s=%v and the announcement %v", n, f, answered[f], ev.Data[f])
		}
	}
	if s := readSettings(t, srv.URL); settingNumber(t, s["seq"]) != last {
		t.Errorf("GET /api/settings carries number %v, want the last announced, %d", s["seq"], last)
	}
}

// A switch typed at a prompt reaches the daemon through the loop's hook
// and is numbered with the rest.
func TestASettingChangedAtAPromptIsNumberedToo(t *testing.T) {
	d, srv, _ := settingsDaemon(t, `{}`)
	before := settingNumber(t, readSettings(t, srv.URL)["seq"])
	stop := announcements(t, d)
	d.Loop.OnSettingsChanged()
	got := stop()
	if len(got) != 1 || settingNumber(t, got[0].Data["seq"]) != before+1 {
		t.Fatalf("announced %v after GET said %d, want one announcement numbered %d", got, before, before+1)
	}
	if after := settingNumber(t, readSettings(t, srv.URL)["seq"]); after != before+1 {
		t.Errorf("GET /api/settings carries %d, want %d", after, before+1)
	}
}

// Changes made at once from several windows: the announcements still go
// out in the order they are numbered, and an answer and the announcement
// with its number say the same thing about every setting.
func TestSettingChangesMadeAtOnceAreAnnouncedInTheOrderTheyAreNumbered(t *testing.T) {
	d, srv, _ := settingsDaemon(t, `{}`)
	stop := announcements(t, d)
	const each = 4
	var (
		wg      sync.WaitGroup
		mu      sync.Mutex
		answers = map[uint64]map[string]any{}
	)
	for i := 0; i < each; i++ {
		for _, w := range []struct{ path, field string }{
			{"/api/settings/keep-going", "keep_going"},
			{"/api/settings/smart-agent", "smart_agent"},
		} {
			on := i%2 == 0
			wg.Add(1)
			go func() {
				defer wg.Done()
				status, answered := postSetting(t, srv.URL+w.path, fmt.Sprintf(`{"enabled":%v}`, on))
				if status != http.StatusOK {
					t.Errorf("POST %s answered %d", w.path, status)
					return
				}
				mu.Lock()
				answers[settingNumber(t, answered["seq"])] = answered
				mu.Unlock()
			}()
		}
	}
	wg.Wait()
	got := stop()
	if len(got) != 2*each {
		t.Fatalf("%d announcements for %d changes", len(got), 2*each)
	}
	var last uint64
	for _, ev := range got {
		n := settingNumber(t, ev.Data["seq"])
		if n != last+1 && last != 0 {
			t.Errorf("announcement %d came after %d", n, last)
		}
		last = n
		answered := answers[n]
		for _, f := range []string{"keep_going", "smart_agent"} {
			if v, ok := answered[f]; ok && !reflect.DeepEqual(v, ev.Data[f]) {
				t.Errorf("number %d: the answer says %s=%v and the announcement %v", n, f, v, ev.Data[f])
			}
		}
	}
}

// A daemon that replaced another names its count anew. A page keeps what
// it has drawn across the reconnect, and compares numbers only within one
// count: the new daemon counts from the start again, and a count taken
// from the clock was below the old one's with a clock set back.
func TestADaemonStartedLaterNamesItsCountAnew(t *testing.T) {
	d1, first, _ := settingsDaemon(t, `{}`)
	stop := announcements(t, d1)
	status, answered := postSetting(t, first.URL+"/api/settings/keep-going", `{"enabled":false}`)
	if status != http.StatusOK {
		t.Fatalf("answered %d", status)
	}
	got := stop()
	before := readSettings(t, first.URL)
	epoch, _ := before["epoch"].(string)
	if epoch == "" {
		t.Fatalf("GET /api/settings carries no epoch: %v", before)
	}
	if answered["epoch"] != epoch || len(got) != 1 || got[0].Data["epoch"] != epoch {
		t.Errorf("the answer (%v) and the announcement (%v) do not carry the epoch GET does (%q)", answered["epoch"], got, epoch)
	}
	_, second, _ := settingsDaemon(t, `{}`)
	if again, _ := readSettings(t, second.URL)["epoch"].(string); again == "" || again == epoch {
		t.Errorf("the later daemon's epoch is %q, the earlier one's %q: want a new one", again, epoch)
	}
}
