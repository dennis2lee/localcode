//go:build gui

package gui

import (
	_ "embed"
	"encoding/binary"
)

// logoICO is the application icon, the same file the installer uses for
// its shortcuts. icon_test.go fails the build if this copy drifts from
// build/icon/localcode.ico.
//
//go:embed logo.ico
var logoICO []byte

// pickIconImage returns the image in a .ico closest to size, or nil if
// the file cannot be read as one.
//
// A .ico is a small directory of independent images. Handing the whole
// file to CreateIconFromResourceEx does not work — it takes one image,
// not the container — so the directory has to be walked here.
func pickIconImage(ico []byte, size int) []byte {
	// ICONDIR: reserved(2) type(2) count(2), then count * 16 bytes.
	if len(ico) < 6 {
		return nil
	}
	if binary.LittleEndian.Uint16(ico[0:2]) != 0 || binary.LittleEndian.Uint16(ico[2:4]) != 1 {
		return nil // not an icon file
	}
	count := int(binary.LittleEndian.Uint16(ico[4:6]))
	if count == 0 || len(ico) < 6+count*16 {
		return nil
	}

	best, bestDelta := []byte(nil), 1<<30
	for i := 0; i < count; i++ {
		e := ico[6+i*16 : 6+(i+1)*16]
		w := int(e[0])
		if w == 0 {
			w = 256 // 0 means 256 in the format
		}
		length := int(binary.LittleEndian.Uint32(e[8:12]))
		offset := int(binary.LittleEndian.Uint32(e[12:16]))
		if length <= 0 || offset < 0 || offset+length > len(ico) {
			continue
		}
		delta := w - size
		if delta < 0 {
			delta = -delta
		}
		if delta < bestDelta {
			best, bestDelta = ico[offset:offset+length], delta
		}
	}
	return best
}
