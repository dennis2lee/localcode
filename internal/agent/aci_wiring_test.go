package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"localcode/internal/config"
	"localcode/internal/events"
	"localcode/internal/tools"
)

// The Smart Agent interface reaches the tools, and reaches them from the
// same pin the roster comes from.
//
// internal/tools has its own tests for what the interface does. These are
// about the wiring: two context keys are set, in two packages that do not
// import each other, and the whole design depends on their never coming
// apart. A turn whose read_file was advertised with offset and limit and
// then executed without them fails in a way nothing in the conversation
// explains.

func longFile(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	var b strings.Builder
	for i := 1; i <= 2000; i++ {
		fmt.Fprintf(&b, "line %d\n", i)
	}
	if err := os.WriteFile(filepath.Join(dir, "long.txt"), []byte(b.String()), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	return dir
}

// readThrough runs read_file through the registry the way a turn does,
// under ctx, and returns what the model would see.
func readThrough(t *testing.T, loop *Loop, ctx context.Context, dir string) string {
	t.Helper()
	ctx = tools.WithWorkingDir(loop.pinSmart(ctx), dir)
	in, _ := json.Marshal(map[string]string{"path": "long.txt"})
	return loop.Tools.Call(ctx, "read_file", in, "").Content
}

func TestTheToolsFollowTheSwitchTheRosterFollows(t *testing.T) {
	srv, _ := smartServer(t)
	defer srv.Close()
	loop := newSmartLoop(t, srv.URL)
	dir := longFile(t)

	off := readThrough(t, loop, context.Background(), dir)
	if strings.Contains(off, "[lines 1-800") {
		t.Errorf("the file was windowed with Smart Agent off:\n%s", lastOf(off))
	}

	loop.SetSmartAgentEnabled(true)
	on := readThrough(t, loop, context.Background(), dir)
	if !strings.Contains(on, "[lines 1-800 of 2000 in long.txt; read on with offset=801]") {
		t.Errorf("the file was not windowed with Smart Agent on:\n%s", lastOf(on))
	}
}

// The pin is what a delegation carries. A sub-agent admitted while the
// switch was on keeps the interface it was admitted with, even though the
// switch has since been turned off — the same rule that keeps it holding a
// roster and a tool allowlist that no longer exist.
func TestAnAdmittedTurnKeepsTheInterfaceItWasAdmittedWith(t *testing.T) {
	srv, _ := smartServer(t)
	defer srv.Close()
	loop := newSmartLoop(t, srv.URL)
	dir := longFile(t)

	// Admitted on, then the user flips the switch off mid-turn.
	admitted := config.WithSmartAgent(context.Background(), true)
	loop.SetSmartAgentEnabled(false)
	if got := readThrough(t, loop, admitted, dir); !strings.Contains(got, "[lines 1-800 of 2000") {
		t.Errorf("a turn admitted with Smart Agent on lost the interface mid-turn:\n%s", lastOf(got))
	}

	// And the other direction: admitted off, switch flipped on.
	admittedOff := config.WithSmartAgent(context.Background(), false)
	loop.SetSmartAgentEnabled(true)
	if got := readThrough(t, loop, admittedOff, dir); strings.Contains(got, "[lines 1-800") {
		t.Errorf("a turn admitted with Smart Agent off gained the interface mid-turn:\n%s", lastOf(got))
	}
}

// The specs the model is shown come from the same context as the calls it
// then makes, so what is advertised and what runs cannot disagree.
func TestTheAdvertisedSchemaAndTheExecutedToolAgree(t *testing.T) {
	srv, _ := smartServer(t)
	defer srv.Close()
	loop := newSmartLoop(t, srv.URL)

	loop.SetSmartAgentEnabled(true)
	ctx := loop.pinSmart(context.Background())
	var found bool
	for _, spec := range loop.Tools.Specs(ctx) {
		if spec.Name != "read_file" {
			continue
		}
		found = true
		if !strings.Contains(string(spec.InputSchema), "offset") {
			t.Errorf("read_file was advertised without paging under Smart Agent: %s", spec.InputSchema)
		}
	}
	if !found {
		t.Fatal("read_file was not advertised at all")
	}

	loop.SetSmartAgentEnabled(false)
	for _, spec := range loop.Tools.Specs(loop.pinSmart(context.Background())) {
		if spec.Name == "read_file" && strings.Contains(string(spec.InputSchema), "offset") {
			t.Errorf("read_file advertised paging with Smart Agent off: %s", spec.InputSchema)
		}
	}
}

// The whole path, once: a turn under Smart Agent asks for read_file, and
// what comes back in the conversation is the windowed result.
//
// The tests above assert the pin and the registry separately. This one is
// the join between them — that the context runTools calls the tool with
// descends from the one sendWithModelText pinned, which is true by
// construction and is exactly the kind of thing that stops being true
// during a refactor.
func TestATurnUnderSmartAgentGetsTheWindowedResult(t *testing.T) {
	dir := longFile(t)
	asked := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		if !asked {
			asked = true
			fmt.Fprint(w, `data: {"choices":[{"delta":{"tool_calls":[{"index":0,"id":"c1","function":{"name":"read_file","arguments":"{\"path\":\"long.txt\"}"}}]}}]}`+"\n\n")
			fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"tool_calls\"}]}\n\n")
		} else {
			fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"read it\"}}]}\n\n")
			fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n")
		}
		fmt.Fprint(w, "data: [DONE]\n\n")
		w.(http.Flusher).Flush()
	}))
	defer srv.Close()

	loop := newSmartLoop(t, srv.URL)
	loop.SetSmartAgentEnabled(true)
	if _, err := loop.Store.CreateSessionIn("s-aci", "", "general-purpose", dir, true); err != nil {
		t.Fatalf("create session: %v", err)
	}
	if err := loop.SendMessage(context.Background(), "s-aci", "general-purpose", "read long.txt"); err != nil {
		t.Fatalf("SendMessage: %v", err)
	}

	evs, err := loop.Store.Events("s-aci", 0)
	if err != nil {
		t.Fatalf("events: %v", err)
	}
	var result string
	for _, e := range evs {
		if e.Type == events.TypeToolEnd {
			if c, ok := e.Data["content"].(string); ok {
				result = c
			}
		}
	}
	if result == "" {
		t.Fatal("the turn never ran read_file")
	}
	if !strings.Contains(result, "[lines 1-800 of 2000 in long.txt; read on with offset=801]") {
		t.Errorf("the result the conversation carries was not windowed:\n%s", lastOf(result))
	}
}

func lastOf(s string) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	if len(lines) > 3 {
		lines = lines[len(lines)-3:]
	}
	return strings.Join(lines, "\n")
}
