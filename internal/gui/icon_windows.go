//go:build gui && windows

package gui

import (
	"unsafe"

	"golang.org/x/sys/windows"
)

// setWindowIcon gives the window its own icon in the title bar, the
// taskbar and Alt-Tab.
//
// Those three come from the window, not from the shortcut that launched
// it: the MSI sets an icon on every shortcut it creates, which is why
// the desktop looks right, and the running window still showed the
// generic default because nothing had ever told it otherwise. A Go
// binary carries no resources for Windows to fall back on either.
//
// So the icon travels inside the executable and is attached at runtime.
// The alternative is a .syso resource object, which would also fix the
// icon Explorer shows for the .exe file itself — but it needs a
// resource compiler in the build, and this needs nothing.
//
// Every failure here is ignored. The worst case is the icon Windows was
// already showing.
func setWindowIcon(hwnd uintptr) {
	if hwnd == 0 {
		return
	}
	user32 := windows.NewLazySystemDLL("user32.dll")
	createIcon := user32.NewProc("CreateIconFromResourceEx")
	sendMessage := user32.NewProc("SendMessageW")

	const (
		wmSetIcon = 0x0080
		iconBig   = 1
		iconSmall = 0
	)
	// Big is the Alt-Tab and taskbar icon, small the title bar's. Both
	// are set from the size closest to what Windows will draw, so
	// neither is a resample of the other.
	for _, want := range []struct {
		size int
		kind uintptr
	}{{32, iconBig}, {16, iconSmall}} {
		img := pickIconImage(logoICO, want.size)
		if img == nil {
			continue
		}
		// LR_DEFAULTCOLOR, and the 0x00030000 is the icon resource
		// version every current Windows expects. fIcon=1 means icon
		// rather than cursor.
		h, _, _ := createIcon.Call(
			uintptr(unsafe.Pointer(&img[0])), uintptr(len(img)),
			1, 0x00030000,
			uintptr(want.size), uintptr(want.size), 0,
		)
		if h == 0 {
			continue
		}
		sendMessage.Call(hwnd, wmSetIcon, want.kind, h)
	}
}
