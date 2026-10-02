package main

import (
	"bytes"
	"encoding/binary"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// Both macOS apps used to be drawn with the generic application icon,
// because neither bundle carried one: no file under Contents/Resources and
// no CFBundleIconFile in Info.plist. The icon is build/icon/localcode.icns,
// committed instead of rendered at release time for the reason
// localcode.ico is (build/icon/make-icns.sh says it). These tests keep both
// ends of that honest: the packaging scripts have to put the file in the
// bundle and name it, and the file has to be a whole icon made from the
// artwork rather than a truncated one or one drawn from something else.

func readRepoFile(t *testing.T, parts ...string) []byte {
	t.Helper()
	path := filepath.Join(append([]string{"..", ".."}, parts...)...)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", filepath.ToSlash(filepath.Join(parts...)), err)
	}
	return data
}

func TestMacAppsCarryTheIcon(t *testing.T) {
	iconKey := regexp.MustCompile(`(?s)<key>CFBundleIconFile</key>\s*<string>AppIcon</string>`)
	for _, script := range []string{"package-mac.sh", "package-mac-gui.sh"} {
		t.Run(script, func(t *testing.T) {
			src := string(readRepoFile(t, "build", script))
			const copyIcon = `cp "$ROOT/build/icon/localcode.icns" "$APP/Contents/Resources/AppIcon.icns"`
			if !strings.Contains(src, copyIcon) {
				t.Errorf("build/%s no longer copies the icon into the bundle (%s): the app would be drawn with the generic icon", script, copyIcon)
			}
			if !iconKey.MatchString(src) {
				t.Errorf("build/%s no longer names the icon in Info.plist (CFBundleIconFile AppIcon): a file in Resources that the plist does not name is not used", script)
			}
		})
	}
}

// What an icon needs to hold: the five sizes macOS asks for, each at 1x and
// 2x. iconutil writes the 16px and 32px 1x images as raw ARGB (ic04, ic05)
// and every other size as a PNG, whose width the type fixes.
var macIconPNGWidths = map[string]int{
	"ic07": 128,  // 128
	"ic08": 256,  // 256
	"ic09": 512,  // 512
	"ic10": 1024, // 512@2x
	"ic11": 32,   // 16@2x
	"ic12": 64,   // 32@2x
	"ic13": 256,  // 128@2x
	"ic14": 512,  // 256@2x
}

func TestMacIconIsAWholeIconMadeFromTheArtwork(t *testing.T) {
	data := readRepoFile(t, "build", "icon", "localcode.icns")
	if len(data) < 8 || string(data[:4]) != "icns" {
		t.Fatalf("build/icon/localcode.icns is not an icns file (starts %q)", data[:min(4, len(data))])
	}
	if got := int(binary.BigEndian.Uint32(data[4:8])); got != len(data) {
		t.Fatalf("build/icon/localcode.icns declares %d bytes and is %d: it was cut short or padded", got, len(data))
	}

	images := map[string][]byte{}
	for off := 8; off < len(data); {
		if off+8 > len(data) {
			t.Fatalf("an element header runs off the end of the file at byte %d", off)
		}
		kind := string(data[off : off+4])
		size := int(binary.BigEndian.Uint32(data[off+4 : off+8]))
		if size < 8 || off+size > len(data) {
			t.Fatalf("element %q at byte %d declares %d bytes, which does not fit in the file", kind, off, size)
		}
		images[kind] = data[off+8 : off+size]
		off += size
	}

	for _, kind := range []string{"ic04", "ic05"} {
		if len(images[kind]) == 0 {
			t.Errorf("no %s element: the 16px or 32px image is missing, and Finder draws those sizes from it", kind)
		}
	}
	for kind, want := range macIconPNGWidths {
		raw, ok := images[kind]
		if !ok {
			t.Errorf("no %s element: the %dpx image is missing", kind, want)
			continue
		}
		cfg, err := png.DecodeConfig(bytes.NewReader(raw))
		if err != nil {
			t.Errorf("%s does not hold a PNG: %v", kind, err)
			continue
		}
		if cfg.Width != want || cfg.Height != want {
			t.Errorf("%s is %dx%d, want %dx%d", kind, cfg.Width, cfg.Height, want, want)
		}
	}

	// The corners of the rounded square are transparent. A renderer that
	// flattens onto white (qlmanage -t does) leaves a white square behind
	// the icon in the Dock.
	for _, kind := range []string{"ic12", "ic07", "ic10"} {
		img, err := png.Decode(bytes.NewReader(images[kind]))
		if err != nil {
			continue // reported above
		}
		if _, _, _, a := img.At(0, 0).RGBA(); a != 0 {
			t.Errorf("%s: the top-left corner is opaque, so the icon has a square background", kind)
		}
	}

	// The ground of the artwork is the fill of its first rect. The middle
	// of the largest image is the doorway, which is ground and not mark.
	// This is what fails if the file was drawn from other artwork, or the
	// artwork was changed and the icon was not rendered again.
	svg := readRepoFile(t, "build", "icon", "icon.svg")
	m := regexp.MustCompile(`<rect[^>]*\sfill="#([0-9a-fA-F]{6})"`).FindSubmatch(svg)
	if m == nil {
		t.Fatal("build/icon/icon.svg has no rect with a fill to compare the icon against")
	}
	rgb, err := strconv.ParseUint(string(m[1]), 16, 32)
	if err != nil {
		t.Fatalf("build/icon/icon.svg: fill #%s is not a colour: %v", m[1], err)
	}
	want := color.NRGBA{R: byte(rgb >> 16), G: byte(rgb >> 8), B: byte(rgb), A: 255}
	big, err := png.Decode(bytes.NewReader(images["ic10"]))
	if err != nil {
		return // reported above
	}
	got := color.NRGBAModel.Convert(big.At(512, 512)).(color.NRGBA)
	if got != want {
		t.Errorf("the middle of the 1024px image is %v, but the artwork's ground is #%s: render the icon again with build/icon/make-icns.sh", got, m[1])
	}
}
