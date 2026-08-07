//go:build gui

package gui

import (
	"bytes"
	"testing"
)

// The window asks for a 16px and a 32px icon separately so neither is a
// resample of the other. That only works if the right image comes back
// for each — and picking the wrong one is invisible in code review and
// only slightly wrong on screen, which is exactly the kind of bug that
// survives.
func TestPickIconImagePicksTheClosestSize(t *testing.T) {
	small := pickIconImage(logoICO, 16)
	big := pickIconImage(logoICO, 32)
	if small == nil || big == nil {
		t.Fatal("the embedded icon yielded no image")
	}
	if bytes.Equal(small, big) {
		t.Error("16px and 32px resolved to the same image; the .ico should carry both")
	}
	// Every image in this .ico is PNG-encoded, which is what
	// CreateIconFromResourceEx is handed.
	for name, img := range map[string][]byte{"16": small, "32": big} {
		if !bytes.HasPrefix(img, []byte("\x89PNG")) {
			t.Errorf("the %spx image is not the PNG the icon file should hold", name)
		}
	}
	// A size no image matches exactly still gets the nearest one rather
	// than nothing — Windows asks for whatever the current DPI wants.
	if got := pickIconImage(logoICO, 20); got == nil {
		t.Error("an unusual size returned no image at all")
	}
}

// Malformed input reaches this from an embedded file that could be
// replaced in a fork or a bad merge. Returning nil costs an icon;
// panicking costs the window.
func TestPickIconImageRejectsWhatIsNotAnIcon(t *testing.T) {
	for name, bad := range map[string][]byte{
		"empty":         {},
		"truncated":     {0, 0, 1, 0},
		"a cursor":      {0, 0, 2, 0, 1, 0},
		"a PNG":         []byte("\x89PNG\r\n\x1a\n"),
		"lying count":   {0, 0, 1, 0, 9, 0},
		"offset beyond": append([]byte{0, 0, 1, 0, 1, 0}, make([]byte, 16)...),
	} {
		if got := pickIconImage(bad, 32); got != nil {
			t.Errorf("%s produced an image: %v", name, got)
		}
	}
}
