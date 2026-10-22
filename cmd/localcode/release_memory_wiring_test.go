package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"localcode/internal/memory"
)

var memoryTools = []string{"memory_save", "memory_read", "memory_search", "memory_forget"}

// The memory tools are what makes memory get used at all: the convention
// they replace asked the model to write with the file tools, which asked
// the person about a path outside the workspace every time, and nothing
// was ever saved. So the daemon and a one-shot run both offer them, and
// neither does with memory off or under --bare.
func TestTheMemoryToolsAreOfferedWhereMemoryIsOn(t *testing.T) {
	for _, c := range []struct {
		name  string
		extra string
		want  bool
	}{
		{"daemon", "", true},
		{"daemon, memory off", `, "auto_memory_enabled": false`, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			f := &fakeModel{}
			smartHome(t, f.server(t).URL, c.extra)
			d, stop, err := buildDaemon(context.Background(), "", nil)
			if err != nil {
				t.Fatalf("build the daemon: %v", err)
			}
			defer stop()
			names := d.Loop.Tools.Names()
			for _, tool := range memoryTools {
				if contains(names, tool) != c.want {
					t.Errorf("%s registered = %v, want %v", tool, !c.want, c.want)
				}
			}
			if (d.Loop.Memory != nil) != c.want {
				t.Errorf("loop memory on = %v, want %v", d.Loop.Memory != nil, c.want)
			}
		})
	}
	for _, c := range []struct {
		name string
		bare bool
		want bool
	}{
		{"run", false, true},
		{"run --bare", true, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			f := &fakeModel{}
			smartHome(t, f.server(t).URL, "")
			if _, err := doRun(t, runOptions{format: formatText, agent: "general-purpose", bare: c.bare}, "x"); err != nil {
				t.Fatal(err)
			}
			got := f.sawTools()
			for _, tool := range memoryTools {
				if contains(got, tool) != c.want {
					t.Errorf("%s offered = %v, want %v", tool, !c.want, c.want)
				}
			}
		})
	}
}

// Starting creates no memory directory: one is created the first time a
// note is saved in it. The old loader created one for every directory a
// daemon was started in, temporary ones included.
func TestStartingCreatesNoMemoryDirectory(t *testing.T) {
	f := &fakeModel{}
	home := smartHome(t, f.server(t).URL, "")
	d, stop, err := buildDaemon(context.Background(), "", nil)
	if err != nil {
		t.Fatalf("build the daemon: %v", err)
	}
	defer stop()
	if d.Loop.Memory == nil {
		t.Fatal("memory is off, so this proves nothing")
	}
	if _, err := os.Stat(filepath.Join(home, ".localcode", "projects")); !os.IsNotExist(err) {
		t.Errorf("a projects directory exists after a start: %v", err)
	}
	if _, err := os.Stat(memory.UserDir(home)); !os.IsNotExist(err) {
		t.Errorf("user memory exists after a start: %v", err)
	}
}
