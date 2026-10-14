package agent

import (
	"context"
	"testing"

	"localcode/internal/events"
	"localcode/internal/provider"
	"localcode/internal/tools"
)

// A synchronous delegation whose turn was cancelled before the child
// started says so in the parent's log. Without the status the row in the
// task panel stays "spawned" for good: nothing else about that child is
// ever written.
func TestASyncDelegationCancelledBeforeItStartsSaysSo(t *testing.T) {
	srv := backgroundServer(t)
	defer srv.Close()
	loop, tasks := newBackgroundLoop(t, srv.URL)
	const sid = "s1"
	if _, err := loop.Store.CreateSession(sid, "", "explore", true); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	childID, _, err := tasks.SpawnSyncInto(ctx, sid, "", "explore", "say hello")
	if err == nil {
		t.Fatal("a delegation from a cancelled turn ran")
	}
	evs, _ := loop.Store.Events(sid, 0)
	var statuses []string
	for _, ev := range evs {
		if id, _ := ev.Data["task_id"].(string); ev.Type == events.TypeTaskStatus && id == childID {
			s, _ := ev.Data["status"].(string)
			statuses = append(statuses, s)
		}
	}
	if childID == "" || len(statuses) != 1 || statuses[0] != "cancelled" {
		t.Errorf("child %q statuses %v, want exactly [cancelled]", childID, statuses)
	}
	if running := tasks.RunningIn([]string{childID}); len(running) != 0 {
		t.Error("the cancelled child is still registered as running")
	}
}

// takeImages is the last check before an image enters the history, which
// every later request carries. A block that is not an image, or an image
// in a type no adapter sends, is left out: a server that refuses it would
// refuse every request after.
func TestTakeImagesLeavesOutWhatTheWireCannotCarry(t *testing.T) {
	ctx := withViewsImages(context.Background(), true)
	used := 0
	png := provider.ImageBlock("image/png", []byte("png"))
	res := tools.Result{Content: "fig", Images: []provider.Block{
		provider.ImageBlock("image/bmp", []byte("bmp")),
		provider.TextBlock("not an image"),
		png,
	}}
	kept := takeImages(ctx, &res, &used)
	if len(kept) != 1 || kept[0].MediaType != "image/png" {
		t.Fatalf("kept %+v, want only the PNG", kept)
	}
	if used != len(png.Data) {
		t.Errorf("counted %d bytes, want the PNG's %d", used, len(png.Data))
	}
	if len(res.Images) != 1 {
		t.Errorf("the result still carries %d images", len(res.Images))
	}
}

// An allowlist of nil means every registered tool. Narrowed for a model
// that cannot see, it has to become every registered tool but view_image:
// an empty list in its place reads as no restriction at all, which hands
// the tool straight back.
func TestWithoutViewImageSpellsOutANilAllowlist(t *testing.T) {
	reg := tools.NewRegistry(nil)
	reg.Register(tools.ReadFile{})
	reg.Register(tools.ViewImage{})
	reg.Register(tools.Glob{})
	got := withoutViewImage(reg, nil)
	want := map[string]bool{"read_file": true, "glob": true}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for _, n := range got {
		if !want[n] {
			t.Errorf("unexpected %q in %v", n, got)
		}
	}
	// And a list it is given keeps its order without view_image.
	if got := withoutViewImage(reg, []string{"glob", tools.ViewImageName, "read_file"}); len(got) != 2 || got[0] != "glob" || got[1] != "read_file" {
		t.Errorf("got %v, want [glob read_file]", got)
	}
}
