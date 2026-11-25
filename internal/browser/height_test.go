package browser

import (
	"bytes"
	"context"
	"image"
	_ "image/jpeg"
	"image/png"
	"math"
	"strings"
	"testing"
	"time"
)

// The panel's frame at the page's width: as tall as fills it, within the
// bounds, and the starting height for a frame with no size.
func TestHeightForFillsTheFrameAtThePagesWidth(t *testing.T) {
	for _, c := range []struct {
		w, h float64
		want int
	}{
		{640, 400, 800},
		{574, 780, 1739},
		{1280, 900, 900},
		{100, 1000, MaxViewportHeight},
		{1000, 100, MinViewportHeight},
		{0, 500, ViewportHeight},
		{500, 0, ViewportHeight},
		{-1, 500, ViewportHeight},
		{math.NaN(), 500, ViewportHeight},
		{500, math.Inf(1), ViewportHeight},
	} {
		if got := HeightFor(c.w, c.h); got != c.want {
			t.Errorf("HeightFor(%v, %v) = %d, want %d", c.w, c.h, got, c.want)
		}
	}
}

// innerHeight waits for the page to be laid out h tall.
func innerHeight(t *testing.T, ctx context.Context, tab *Tab, h int) {
	t.Helper()
	var got int
	for deadline := time.Now().Add(15 * time.Second); time.Now().Before(deadline); time.Sleep(50 * time.Millisecond) {
		if err := tab.eval(ctx, `innerWidth === 1280 ? innerHeight : -innerWidth`, &got); err == nil && got == h {
			return
		}
	}
	t.Fatalf("the page is laid out %d tall (negative: that wide), want %d", got, h)
}

// frameOf waits for a frame of the stream that is w by h.
func frameOf(t *testing.T, frames <-chan []byte, w, h int) {
	t.Helper()
	timeout := time.After(20 * time.Second)
	var last image.Config
	for {
		select {
		case f := <-frames:
			if c, _, err := image.DecodeConfig(bytes.NewReader(f)); err == nil {
				last = c
				if c.Width == w && c.Height == h {
					return
				}
			}
		case <-timeout:
			t.Fatalf("no %dx%d frame; the last was %dx%d", w, h, last.Width, last.Height)
		}
	}
}

// A conversation's pages are as tall as its panel asks for: the tab it
// has, the tabs it opens after, the one a link opens, its screenshots
// and the frames the panel is sent. Another conversation's are not.
func TestAConversationsPagesAreAsTallAsItsPanelAsks(t *testing.T) {
	m := testManager(t)
	ctx := ctxFor(t)
	site := testSite(t)
	if h := m.Height("c1"); h != ViewportHeight {
		t.Fatalf("a conversation no panel has asked for is %d tall, want %d", h, ViewportHeight)
	}
	// Asked before it has a tab: its first one opens at it.
	if h, err := m.SetHeight(ctx, "c1", 1600); h != 1600 || err != nil {
		t.Fatalf("SetHeight = %d, %v", h, err)
	}
	tab, err := m.Tab(ctx, "c1")
	if err != nil {
		t.Fatal(err)
	}
	if err := tab.Navigate(ctx, site); err != nil {
		t.Fatal(err)
	}
	innerHeight(t, ctx, tab, 1600)
	shot, err := tab.Capture(ctx, Shot{})
	if err != nil {
		t.Fatal(err)
	}
	c, err := png.DecodeConfig(bytes.NewReader(shot.PNG))
	if err != nil || c.Width != ViewportWidth || c.Height != 1600 || shot.Width != c.Width || shot.Height != c.Height {
		t.Fatalf("a screenshot is %dx%d and says %dx%d (%v), want 1280x1600", c.Width, c.Height, shot.Width, shot.Height, err)
	}

	// The stream is the page at its size, not scaled down to fit 800.
	frames, stop, err := tab.Watch(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer stop()
	frameOf(t, frames, ViewportWidth, 1600)
	// And follows it while it is watched.
	if h, err := m.SetHeight(ctx, "c1", 1000); h != 1000 || err != nil {
		t.Fatalf("SetHeight = %d, %v", h, err)
	}
	if h := tab.Height(); h != 1000 {
		t.Errorf("the tab says it is %d tall, want 1000", h)
	}
	innerHeight(t, ctx, tab, 1000)
	frameOf(t, frames, ViewportWidth, 1000)

	// A click is held to the page as tall as it is now.
	if err := tab.Click(ctx, 10, 1200, 1); err == nil || !strings.Contains(err.Error(), "outside the 1280x1000 page") {
		t.Errorf("a click below the page: %v", err)
	}
	if err := tab.Click(ctx, 10, 990, 1); err != nil {
		t.Errorf("a click near the bottom of the page: %v", err)
	}

	// Another conversation is as tall as its own panel asks, and starts
	// at the starting height; one held to the bounds says so.
	other, err := m.Tab(ctx, "c2")
	if err != nil {
		t.Fatal(err)
	}
	innerHeight(t, ctx, other, ViewportHeight)
	if h, err := m.SetHeight(ctx, "c2", 10); h != MinViewportHeight || err != nil {
		t.Fatalf("SetHeight below the bounds = %d, %v", h, err)
	}
	innerHeight(t, ctx, other, MinViewportHeight)
	innerHeight(t, ctx, tab, 1000)

	// A tab the conversation opens after, and one its page's link opens.
	second, err := m.NewTab(ctx, "c1")
	if err != nil {
		t.Fatal(err)
	}
	innerHeight(t, ctx, second, 1000)
	if err := second.Navigate(ctx, site); err != nil {
		t.Fatal(err)
	}
	snap, err := second.Read(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := second.ClickRef(ctx, refNamed(t, snap, "open second")); err != nil {
		t.Fatal(err)
	}
	var linked *Tab
	for deadline := time.Now().Add(15 * time.Second); time.Now().Before(deadline); time.Sleep(100 * time.Millisecond) {
		if linked = m.Existing("c1"); linked != second {
			break
		}
	}
	if linked == nil || linked == second {
		t.Fatal("the conversation did not move to the tab the link opened")
	}
	if h := linked.Height(); h != 1000 {
		t.Errorf("the link's tab says it is %d tall, want 1000", h)
	}
	innerHeight(t, ctx, linked, 1000)
}
