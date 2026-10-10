package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"localcode/internal/config"
	"localcode/internal/provider"
	"localcode/internal/tools"
)

// A model that can see is shown an image file it is pointed at.
//
// It used to be shown only the images a person pasted. A "vision" agent
// handed a path, which is all a delegation can hand over, had no way to
// open it and delegated the request to "vision" again.

var testPNG = append([]byte("\x89PNG\r\n\x1a\n"), bytes.Repeat([]byte{7}, 64)...)

func writePNG(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "fig.png")
	if err := os.WriteFile(path, testPNG, 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// imageLoop is a scripted loop on one profile whose model is model, with
// view_image and read_file registered.
func imageLoop(t *testing.T, model string, vision *bool, turns ...[]provider.StreamEvent) (*Loop, *scriptedProvider, string) {
	t.Helper()
	reg := tools.NewRegistry(nil)
	reg.Register(tools.ReadFile{})
	reg.Register(tools.ViewImage{})
	p := &scriptedProvider{turns: turns}
	loop, sessionID := scriptedLoop(t, p, reg)
	loop.Config.Profiles["balanced"] = config.Profile{Provider: "local", Model: model, Vision: vision}
	return loop, p, sessionID
}

func offersTool(req provider.ChatRequest, name string) bool {
	for _, tool := range req.Tools {
		if tool.Name == name {
			return true
		}
	}
	return false
}

func requestsOf(p *scriptedProvider) []provider.ChatRequest {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]provider.ChatRequest(nil), p.requests...)
}

func TestAnImageFileIsShownAfterTheResultThatOpenedIt(t *testing.T) {
	path := writePNG(t)
	input, _ := json.Marshal(map[string]string{"path": path})
	loop, p, sessionID := imageLoop(t, "claude-sonnet-5-5", nil,
		toolCall("c1", "view_image", string(input)),
		textReply("A bar chart."),
	)
	if err := loop.SendMessage(context.Background(), sessionID, "general-purpose", "what is in fig.png?"); err != nil {
		t.Fatalf("SendMessage: %v", err)
	}
	reqs := requestsOf(p)
	if len(reqs) != 2 {
		t.Fatalf("%d requests, want 2", len(reqs))
	}
	if !offersTool(reqs[0], tools.ViewImageName) {
		t.Fatal("a Claude model was not offered view_image")
	}
	last := reqs[1].Messages[len(reqs[1].Messages)-1]
	if last.Role != provider.RoleUser || len(last.Content) != 2 {
		t.Fatalf("the message after the call holds %d blocks, want the result and the image: %+v", len(last.Content), last.Content)
	}
	if b := last.Content[0]; b.Type != provider.BlockToolResult || b.ToolUseID != "c1" || b.IsError {
		t.Errorf("first block = %s %s error=%v, want the call's result", b.Type, b.ToolUseID, b.IsError)
	}
	if b := last.Content[1]; b.Type != provider.BlockImage || b.MediaType != "image/png" || !bytes.Equal(b.Data, testPNG) {
		t.Errorf("second block = %s %s %d bytes, want the PNG", b.Type, b.MediaType, len(b.Data))
	}

	// A restart sends the same thing: the image is in the log with the
	// result, and comes back after it.
	evs, err := loop.Store.Events(sessionID, 0)
	if err != nil {
		t.Fatal(err)
	}
	live, rebuilt := sendableHistory(loop.history(sessionID)), sendableHistory(rehydrateHistory(evs))
	if len(live) != len(rebuilt) {
		t.Fatalf("live %d messages, rebuilt %d", len(live), len(rebuilt))
	}
	for i := range live {
		if len(live[i].Content) != len(rebuilt[i].Content) {
			t.Fatalf("message %d: live %d blocks, rebuilt %d", i, len(live[i].Content), len(rebuilt[i].Content))
		}
		for j, b := range live[i].Content {
			r := rebuilt[i].Content[j]
			if b.Type != r.Type || b.MediaType != r.MediaType || !bytes.Equal(b.Data, r.Data) || b.ToolUseID != r.ToolUseID {
				t.Errorf("message %d block %d: live %s %s %d bytes, rebuilt %s %s %d bytes",
					i, j, b.Type, b.MediaType, len(b.Data), r.Type, r.MediaType, len(r.Data))
			}
		}
	}
}

// A model that cannot see is not offered the tool, and one that was given
// it anyway, by an agent's own tool list, is not sent the image: an image
// its server refuses would stay in the history and be refused every turn.
func TestAModelThatCannotSeeIsNotSentAnImage(t *testing.T) {
	path := writePNG(t)
	input, _ := json.Marshal(map[string]string{"path": path})

	loop, p, sessionID := imageLoop(t, "deepseek-v4-flash", nil, textReply("ok"))
	if err := loop.SendMessage(context.Background(), sessionID, "general-purpose", "hi"); err != nil {
		t.Fatal(err)
	}
	if offersTool(requestsOf(p)[0], tools.ViewImageName) {
		t.Error("a model with no vision setting and no Claude in its name was offered view_image")
	}

	loop, p, sessionID = imageLoop(t, "deepseek-v4-flash", nil,
		toolCall("c1", "view_image", string(input)),
		textReply("ok"),
	)
	loop.Config.Agents["general-purpose"] = config.AgentConfig{Profile: "balanced", Tools: []string{"read_file", "view_image"}}
	if err := loop.SendMessage(context.Background(), sessionID, "general-purpose", "look"); err != nil {
		t.Fatal(err)
	}
	reqs := requestsOf(p)
	res, ok := resultFor(reqs[1].Messages, "c1")
	if !ok || !res.IsError || !strings.Contains(res.ToolResultContent, `"vision": true`) {
		t.Errorf("result = %+v (found %v), want an error saying how to turn vision on", res, ok)
	}
	for _, m := range reqs[1].Messages {
		for _, b := range m.Content {
			if b.Type == provider.BlockImage {
				t.Fatal("an image was sent to a model that cannot see")
			}
		}
	}
}

// The setting decides, both ways.
func TestTheVisionSettingDecidesWhoIsOfferedTheTool(t *testing.T) {
	yes, no := true, false
	cases := []struct {
		name   string
		model  string
		vision *bool
		want   bool
	}{
		{"a Claude model by default", "us.anthropic.claude-sonnet-5-5", nil, true},
		{"anything else by default", "qwen3-coder", nil, false},
		{"set on", "qwen2.5-vl", &yes, true},
		{"set off on Claude", "claude-sonnet-5-5", &no, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			loop, p, sessionID := imageLoop(t, c.model, c.vision, textReply("ok"))
			if err := loop.SendMessage(context.Background(), sessionID, "general-purpose", "hi"); err != nil {
				t.Fatal(err)
			}
			if got := offersTool(requestsOf(p)[0], tools.ViewImageName); got != c.want {
				t.Errorf("offered = %v, want %v", got, c.want)
			}
		})
	}
}

// The images of one step travel in one message, and a message over the
// limit is refused whole, so an image that would cross it is left out and
// said, and the others go.
func TestAnImageThatWouldPassTheMessageLimitIsLeftOut(t *testing.T) {
	ctx := withViewsImages(context.Background(), true)
	used := provider.MaxImageBytesPerMessage - 10
	res := tools.Result{Content: "fig.png", Images: []provider.Block{provider.ImageBlock("image/png", make([]byte, 64))}}
	if kept := takeImages(ctx, &res, &used); len(kept) != 0 {
		t.Errorf("%d images kept past the limit", len(kept))
	}
	if !res.IsError || !strings.Contains(res.Content, "not attached") {
		t.Errorf("result = %+v, want it to say the image was left out", res)
	}

	used = 0
	res = tools.Result{Content: "fig.png", Images: []provider.Block{provider.ImageBlock("image/png", make([]byte, 64))}}
	if kept := takeImages(ctx, &res, &used); len(kept) != 1 || used != 64 || res.IsError {
		t.Errorf("kept %d, used %d, error %v: an image under the limit was not attached", len(kept), used, res.IsError)
	}
}

// The delegation tools say which agents can be handed an image, so a model
// that cannot see knows whom to give the path to.
func TestTheAgentListSaysWhoCanViewImages(t *testing.T) {
	loop, p, sessionID := delegationLoop(t, [][]provider.StreamEvent{textReply("ok")}, "general-purpose", "vision")
	yes := true
	loop.Config.Profiles["seeing"] = config.Profile{Provider: "local", Model: "qwen2.5-vl", Vision: &yes}
	loop.Config.Agents["vision"] = config.AgentConfig{Profile: "seeing", Description: "Looks at images."}
	if err := loop.SendMessage(context.Background(), sessionID, "general-purpose", "hi"); err != nil {
		t.Fatal(err)
	}
	var desc string
	for _, tool := range requestsOf(p)[0].Tools {
		if tool.Name == "Task" {
			desc = tool.Description
		}
	}
	for _, line := range strings.Split(desc, "\n") {
		switch {
		case strings.HasPrefix(line, "- vision:") && !strings.Contains(line, "Can view image files"):
			t.Errorf("the agent that can see is not marked: %q", line)
		case strings.HasPrefix(line, "- general-purpose:") && strings.Contains(line, "Can view image files"):
			t.Errorf("an agent that cannot see is marked: %q", line)
		}
	}
	if !strings.Contains(desc, "- vision:") {
		t.Fatalf("no vision line in %q", desc)
	}
}

// opencode's read opens images, so an agent whose tools block switches
// read off has view_image switched off with it.
func TestSwitchingReadOffSwitchesViewImageOff(t *testing.T) {
	loop, _, _ := imageLoop(t, "claude-sonnet-5-5", nil)
	// A tool the switch leaves alone, as every real registry has one: an
	// allowlist with nothing left in it reads as no restriction at all.
	loop.Tools.Register(tools.Glob{})
	ctx := withViewsImages(context.Background(), true)
	allowed := loop.toolsForTurn(ctx, config.AgentConfig{Profile: "balanced", ToolSwitches: map[string]bool{"read": false}})
	for _, name := range []string{"read_file", tools.ViewImageName} {
		if tools.IsAllowed(allowed, name) {
			t.Errorf("%s is allowed for an agent that switched read off", name)
		}
	}
}
