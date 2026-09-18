package agent

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"localcode/internal/config"
	"localcode/internal/events"
	"localcode/internal/provider"
	"localcode/internal/session"
	"localcode/internal/tools"
)

type validatingProvider struct {
	*scriptedProvider
}

func (v *validatingProvider) Chat(ctx context.Context, req provider.ChatRequest) (<-chan provider.StreamEvent, error) {
	if err := provider.ValidateRequestImages(req); err != nil {
		return nil, err
	}
	return v.scriptedProvider.Chat(ctx, req)
}

func newTestLoop(store *session.Store, p provider.Provider) *Loop {
	cfg := &config.Config{
		Providers: map[string]config.ProviderConfig{
			"mock": {Type: config.ProviderOpenAICompat},
		},
		Profiles: map[string]config.Profile{
			"default": {Provider: "mock", Model: "mock-model"},
		},
		Agents: map[string]config.AgentConfig{
			"general-purpose": {Profile: "default"},
		},
		DefaultProfile: "default",
	}
	registry := tools.NewRegistry(nil)
	return New(store, registry, map[string]provider.Provider{"mock": p}, cfg)
}

// TestAnImageRoundTripsThroughTheSessionLogAcrossRestart verifies that an
// image sent in a user message is persisted to the session event log and
// restored with identical media type and data bytes across daemon restarts.
func TestAnImageRoundTripsThroughTheSessionLogAcrossRestart(t *testing.T) {
	dir := t.TempDir()
	store, err := session.NewStore(dir)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	t.Cleanup(store.Close)

	const sid = "session-image-roundtrip"
	if _, err := store.CreateSession(sid, "", "general-purpose", true); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	p := &validatingProvider{scriptedProvider: scriptedReply("I see your test image.")}
	loop := newTestLoop(store, p)

	imageBytes := []byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n', 0x00, 0x11, 0x22, 0x33}
	imgBlock := provider.Block{
		Type:      provider.BlockImage,
		MediaType: "image/png",
		Data:      imageBytes,
	}

	if err := loop.SendMessage(context.Background(), sid, "general-purpose", "describe image", imgBlock); err != nil {
		t.Fatalf("SendMessage with image: %v", err)
	}

	// Verify the stored event carries images with base64 encoded data.
	evs, err := store.Events(sid, 0)
	if err != nil {
		t.Fatalf("Store.Events: %v", err)
	}
	var userEv *events.Event
	for i := range evs {
		if evs[i].Type == events.TypeUserMessage {
			userEv = &evs[i]
			break
		}
	}
	if userEv == nil {
		t.Fatalf("expected TypeUserMessage event in store, found none")
	}
	if userEv.Data["images"] == nil {
		t.Fatalf("expected images in event Data, got nil")
	}

	// Simulate a daemon restart by reloading store from disk into a fresh Loop.
	restoredStore, warnings, err := session.LoadAllFromDisk(dir)
	if err != nil {
		t.Fatalf("LoadAllFromDisk: %v", err)
	}
	t.Cleanup(restoredStore.Close)
	if len(warnings) != 0 {
		t.Fatalf("unexpected warnings on load: %v", warnings)
	}

	restoredP := &validatingProvider{scriptedProvider: scriptedReply("ready.")}
	restoredLoop := newTestLoop(restoredStore, restoredP)
	restoredLoop.RehydrateAll()

	history := restoredLoop.history(sid)
	if len(history) < 2 {
		t.Fatalf("rehydrated history has %d messages, want at least 2", len(history))
	}

	userMsg := history[0]
	if userMsg.Role != provider.RoleUser {
		t.Errorf("history[0].Role = %q, want user", userMsg.Role)
	}
	if len(userMsg.Content) != 2 {
		t.Fatalf("userMsg.Content length = %d, want 2 (text + image)", len(userMsg.Content))
	}

	textBlock := userMsg.Content[0]
	if textBlock.Type != provider.BlockText || textBlock.Text != "describe image" {
		t.Errorf("userMsg text block = %+v, want TextBlock(\"describe image\")", textBlock)
	}

	restoredImg := userMsg.Content[1]
	if restoredImg.Type != provider.BlockImage {
		t.Errorf("restoredImg.Type = %q, want %q", restoredImg.Type, provider.BlockImage)
	}
	if restoredImg.MediaType != "image/png" {
		t.Errorf("restoredImg.MediaType = %q, want \"image/png\"", restoredImg.MediaType)
	}
	if !bytes.Equal(restoredImg.Data, imageBytes) {
		t.Errorf("restoredImg.Data = %v, want %v", restoredImg.Data, imageBytes)
	}
}

// TestALogWrittenWithoutAnImageRehydratesUnchanged checks backward compatibility
// when reading an older event log or image-free conversation.
func TestALogWrittenWithoutAnImageRehydratesUnchanged(t *testing.T) {
	log := []events.Event{
		{Type: events.TypeUserMessage, Data: map[string]any{"text": "just text"}},
		{Type: events.TypeMessagePartEnd, Data: map[string]any{"text": "assistant reply"}},
	}

	history := rehydrateHistory(log)
	if len(history) != 2 {
		t.Fatalf("history length = %d, want 2", len(history))
	}
	if len(history[0].Content) != 1 {
		t.Fatalf("history[0].Content length = %d, want 1", len(history[0].Content))
	}
	if history[0].Content[0].Type != provider.BlockText || history[0].Content[0].Text != "just text" {
		t.Errorf("history[0].Content[0] = %+v, want BlockText(\"just text\")", history[0].Content[0])
	}
}

// TestAnOlderBuildIgnoringImagesDoesNotCrash verifies that older code ignoring
// the images field can parse new event logs containing images without error.
func TestAnOlderBuildIgnoringImagesDoesNotCrash(t *testing.T) {
	// Simulate an older build unmarshaling a new event log line with images.
	encodedImage := base64.StdEncoding.EncodeToString([]byte("fake-png-data"))
	jsonLine := fmt.Sprintf(`{"seq":1,"session":"s1","type":"message.user","timestamp":"2026-09-18T12:00:00Z","data":{"text":"hello with image","images":[{"media_type":"image/png","data":"%s"}]}}`, encodedImage)

	var ev events.Event
	if err := json.Unmarshal([]byte(jsonLine), &ev); err != nil {
		t.Fatalf("Unmarshal new event on older build: %v", err)
	}

	// An older build only reads "text" and "model_text", ignoring "images".
	text := ev.Data["text"].(string)
	if text != "hello with image" {
		t.Errorf("ev.Data[\"text\"] = %q, want \"hello with image\"", text)
	}
}

// TestCompactionDropsImagesAndRecordsTheirCountInTheSummary verifies that
// compaction strips raw image blocks and appends an explicit dropped count notice.
func TestCompactionDropsImagesAndRecordsTheirCountInTheSummary(t *testing.T) {
	t.Run("single image dropped", func(t *testing.T) {
		dir := t.TempDir()
		store, err := session.NewStore(dir)
		if err != nil {
			t.Fatalf("NewStore: %v", err)
		}
		t.Cleanup(store.Close)

		const sid = "session-compact-1"
		if _, err := store.CreateSession(sid, "", "general-purpose", true); err != nil {
			t.Fatalf("CreateSession: %v", err)
		}

		p := &validatingProvider{scriptedProvider: &scriptedProvider{turns: [][]provider.StreamEvent{
			{
				{Type: provider.EventTextDelta, TextDelta: "seen 1"},
				{Type: provider.EventMessageStop, StopReason: "end_turn"},
			},
			{
				{Type: provider.EventTextDelta, TextDelta: "Summary of discussion."},
				{Type: provider.EventMessageStop, StopReason: "end_turn"},
			},
		}}}
		loop := newTestLoop(store, p)

		img := provider.Block{Type: provider.BlockImage, MediaType: "image/png", Data: []byte("img-1")}
		if err := loop.SendMessage(context.Background(), sid, "general-purpose", "check 1", img); err != nil {
			t.Fatalf("SendMessage: %v", err)
		}

		if err := loop.SendMessage(context.Background(), sid, "general-purpose", "/compact"); err != nil {
			t.Fatalf("SendMessage /compact: %v", err)
		}

		// Inspect post-compaction history in loop.
		history := loop.history(sid)
		if len(history) != 1 {
			t.Fatalf("history length = %d, want 1", len(history))
		}
		summaryBlock := history[0].Content[0]
		wantNote := "[1 image was omitted from this summary and is no longer in this conversation.]"
		if !strings.Contains(summaryBlock.Text, wantNote) {
			t.Errorf("summaryBlock.Text = %q, want it to contain %q", summaryBlock.Text, wantNote)
		}

		// Confirm that no BlockImage blocks remain in history.
		for _, blk := range history[0].Content {
			if blk.Type == provider.BlockImage {
				t.Errorf("found BlockImage in post-compaction history")
			}
		}

		// Verify the summarization request sent to provider had image converted to text placeholder.
		reqs := p.sentRequests()
		if len(reqs) < 2 {
			t.Fatalf("expected at least 2 requests (turn + compact), got %d", len(reqs))
		}
		compactReq := reqs[len(reqs)-1]
		var foundPlaceholder bool
		for _, m := range compactReq.Messages {
			for _, b := range m.Content {
				if b.Type == provider.BlockImage {
					t.Errorf("found raw BlockImage in compaction request payload")
				}
				if strings.Contains(b.Text, "[image: image/png]") {
					foundPlaceholder = true
				}
			}
		}
		if !foundPlaceholder {
			t.Errorf("compactReq did not contain placeholder \"[image: image/png]\"")
		}
	})

	t.Run("multiple images dropped", func(t *testing.T) {
		dir := t.TempDir()
		store, err := session.NewStore(dir)
		if err != nil {
			t.Fatalf("NewStore: %v", err)
		}
		t.Cleanup(store.Close)

		const sid = "session-compact-multi"
		if _, err := store.CreateSession(sid, "", "general-purpose", true); err != nil {
			t.Fatalf("CreateSession: %v", err)
		}

		p := &validatingProvider{scriptedProvider: &scriptedProvider{turns: [][]provider.StreamEvent{
			{
				{Type: provider.EventTextDelta, TextDelta: "reply 1"},
				{Type: provider.EventMessageStop, StopReason: "end_turn"},
			},
			{
				{Type: provider.EventTextDelta, TextDelta: "reply 2"},
				{Type: provider.EventMessageStop, StopReason: "end_turn"},
			},
			{
				{Type: provider.EventTextDelta, TextDelta: "Multi-summary."},
				{Type: provider.EventMessageStop, StopReason: "end_turn"},
			},
		}}}
		loop := newTestLoop(store, p)

		img1 := provider.Block{Type: provider.BlockImage, MediaType: "image/png", Data: []byte("img-1")}
		img2 := provider.Block{Type: provider.BlockImage, MediaType: "image/jpeg", Data: []byte("img-2")}
		img3 := provider.Block{Type: provider.BlockImage, MediaType: "image/webp", Data: []byte("img-3")}

		if err := loop.SendMessage(context.Background(), sid, "general-purpose", "turn 1", img1); err != nil {
			t.Fatalf("SendMessage 1: %v", err)
		}
		if err := loop.SendMessage(context.Background(), sid, "general-purpose", "turn 2", img2, img3); err != nil {
			t.Fatalf("SendMessage 2: %v", err)
		}
		if err := loop.SendMessage(context.Background(), sid, "general-purpose", "/compact"); err != nil {
			t.Fatalf("SendMessage /compact: %v", err)
		}

		history := loop.history(sid)
		summaryBlock := history[0].Content[0]
		wantNote := "[3 images were omitted from this summary and is no longer in this conversation.]"
		if !strings.Contains(summaryBlock.Text, "[3 images were omitted from this summary and are no longer in this conversation.]") {
			t.Errorf("summaryBlock.Text = %q, want note for 3 omitted images: %q", summaryBlock.Text, wantNote)
		}
	})
}

// TestAnImageAfterCompactionSurvivesWholeWhileAnImageBeforeItIsGone tests that
// images prior to compaction are omitted while images attached after compaction
// survive rehydration across restart intact.
func TestAnImageAfterCompactionSurvivesWholeWhileAnImageBeforeItIsGone(t *testing.T) {
	dir := t.TempDir()
	store, err := session.NewStore(dir)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	t.Cleanup(store.Close)

	const sid = "session-survive-after-compact"
	if _, err := store.CreateSession(sid, "", "general-purpose", true); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	p := &validatingProvider{scriptedProvider: &scriptedProvider{turns: [][]provider.StreamEvent{
		{
			{Type: provider.EventTextDelta, TextDelta: "pre-compact reply"},
			{Type: provider.EventMessageStop, StopReason: "end_turn"},
		},
		{
			{Type: provider.EventTextDelta, TextDelta: "Summary text."},
			{Type: provider.EventMessageStop, StopReason: "end_turn"},
		},
		{
			{Type: provider.EventTextDelta, TextDelta: "post-compact reply"},
			{Type: provider.EventMessageStop, StopReason: "end_turn"},
		},
	}}}
	loop := newTestLoop(store, p)

	imgBefore := provider.Block{Type: provider.BlockImage, MediaType: "image/png", Data: []byte("BEFORE_BYTES")}
	imgAfter := provider.Block{Type: provider.BlockImage, MediaType: "image/jpeg", Data: []byte("AFTER_BYTES")}

	// 1. Message with image before compaction.
	if err := loop.SendMessage(context.Background(), sid, "general-purpose", "before cut", imgBefore); err != nil {
		t.Fatalf("SendMessage before cut: %v", err)
	}

	// 2. Compaction cut.
	if err := loop.SendMessage(context.Background(), sid, "general-purpose", "/compact"); err != nil {
		t.Fatalf("SendMessage /compact: %v", err)
	}

	// 3. Message with image after compaction.
	if err := loop.SendMessage(context.Background(), sid, "general-purpose", "after cut", imgAfter); err != nil {
		t.Fatalf("SendMessage after cut: %v", err)
	}

	// Simulate restart.
	restoredStore, warnings, err := session.LoadAllFromDisk(dir)
	if err != nil {
		t.Fatalf("LoadAllFromDisk: %v", err)
	}
	t.Cleanup(restoredStore.Close)
	if len(warnings) != 0 {
		t.Fatalf("warnings: %v", warnings)
	}

	restoredP := &validatingProvider{scriptedProvider: scriptedReply("continue")}
	restoredLoop := newTestLoop(restoredStore, restoredP)
	restoredLoop.RehydrateAll()

	history := restoredLoop.history(sid)
	// history has 3 messages:
	// 0: user summary
	// 1: user "after cut" turn with imgAfter
	// 2: assistant "post-compact reply"
	if len(history) != 3 {
		t.Fatalf("expected 3 history messages, got %d: %+v", len(history), history)
	}

	// Message 0: summary. Must NOT contain imgBefore, but must mention 1 image was omitted.
	summaryContent := history[0].Content[0].Text
	if !strings.Contains(summaryContent, "[1 image was omitted from this summary and is no longer in this conversation.]") {
		t.Errorf("summary does not mention omitted image: %q", summaryContent)
	}
	for _, b := range history[0].Content {
		if b.Type == provider.BlockImage {
			t.Errorf("image found in compaction summary message")
		}
	}

	// Message 1: "after cut" user turn. Must contain imgAfter intact.
	userAfterMsg := history[1]
	if userAfterMsg.Role != provider.RoleUser {
		t.Errorf("history[1].Role = %q, want user", userAfterMsg.Role)
	}
	var foundAfterImage bool
	for _, b := range userAfterMsg.Content {
		if b.Type == provider.BlockImage {
			foundAfterImage = true
			if b.MediaType != "image/jpeg" {
				t.Errorf("b.MediaType = %q, want \"image/jpeg\"", b.MediaType)
			}
			if !bytes.Equal(b.Data, []byte("AFTER_BYTES")) {
				t.Errorf("b.Data = %v, want AFTER_BYTES", b.Data)
			}
		}
	}
	if !foundAfterImage {
		t.Errorf("imgAfter was not found in post-compaction turn after rehydration")
	}

	// Message 2: assistant reply.
	if history[2].Role != provider.RoleAssistant {
		t.Errorf("history[2].Role = %q, want assistant", history[2].Role)
	}
}

// TestRehydratedOversizedImageCannotBypassTenMegabyteLimit ensures that an
// oversized image in an event log is validated at the provider boundary when
// history is rehydrated and used in subsequent model requests.
func TestRehydratedOversizedImageCannotBypassTenMegabyteLimit(t *testing.T) {
	dir := t.TempDir()
	store, err := session.NewStore(dir)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	t.Cleanup(store.Close)

	const sid = "session-oversized"
	if _, err := store.CreateSession(sid, "", "general-purpose", true); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	// Construct an oversized image (11MB) directly in the event log.
	oversizedBytes := make([]byte, 11*1024*1024)
	oversizedBytes[0] = 0x89
	copy(oversizedBytes[1:4], "PNG")
	store.Append(sid, events.TypeUserMessage, map[string]any{
		"text": "oversized image prompt",
		"images": []events.Image{
			{MediaType: "image/png", Data: oversizedBytes},
		},
	})
	store.Append(sid, events.TypeMessagePartEnd, map[string]any{
		"text": "recorded reply",
	})

	// Rehydrate in a fresh Loop.
	restoredStore, _, err := session.LoadAllFromDisk(dir)
	if err != nil {
		t.Fatalf("LoadAllFromDisk: %v", err)
	}
	t.Cleanup(restoredStore.Close)

	p := &validatingProvider{scriptedProvider: scriptedReply("should not reach here")}
	loop := newTestLoop(restoredStore, p)
	loop.RehydrateAll()

	// Subsequent turn attempts to send request with rehydrated history to provider.
	err = loop.SendMessage(context.Background(), sid, "general-purpose", "next prompt")
	if err == nil {
		t.Fatalf("expected error from provider boundary for oversized image in history, got nil")
	}
	if !strings.Contains(err.Error(), "10MB limit") {
		t.Errorf("err = %q, want it to cite the 10MB limit", err.Error())
	}
}

// TestAConversationWithNoImagesRehydratesToExactlyWhatItDidBefore verifies zero
// regression on image-free conversations.
func TestAConversationWithNoImagesRehydratesToExactlyWhatItDidBefore(t *testing.T) {
	log := []events.Event{
		{Type: events.TypeUserMessage, Data: map[string]any{"text": "step 1"}},
		{Type: events.TypeToolStart, Data: map[string]any{"tool_use_id": "call_1", "name": "read_file"}},
		{Type: events.TypeToolEnd, Data: map[string]any{"tool_use_id": "call_1", "content": "contents", "input": `{"path":"f.txt"}`}},
		{Type: events.TypeMessagePartEnd, Data: map[string]any{"text": "step 1 done"}},
		{Type: events.TypeUserMessage, Data: map[string]any{"text": "step 2"}},
		{Type: events.TypeMessagePartEnd, Data: map[string]any{"text": "step 2 done"}},
	}

	history := rehydrateHistory(log)
	if len(history) != 6 {
		t.Fatalf("history length = %d, want 6", len(history))
	}

	for _, m := range history {
		for _, b := range m.Content {
			if b.Type == provider.BlockImage {
				t.Fatalf("unexpected BlockImage in image-free conversation")
			}
		}
	}
	if history[0].Content[0].Text != "step 1" {
		t.Errorf("history[0] text = %q, want \"step 1\"", history[0].Content[0].Text)
	}
	if history[4].Content[0].Text != "step 2" {
		t.Errorf("history[4] text = %q, want \"step 2\"", history[4].Content[0].Text)
	}
	if history[5].Content[0].Text != "step 2 done" {
		t.Errorf("history[5] text = %q, want \"step 2 done\"", history[5].Content[0].Text)
	}
}
