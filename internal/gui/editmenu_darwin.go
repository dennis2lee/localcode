//go:build gui && darwin

package gui

/*
#cgo CFLAGS: -x objective-c
#cgo LDFLAGS: -framework Cocoa
#import <Cocoa/Cocoa.h>

// lcInstallEditMenu puts the standard Edit menu on the application menu
// bar: Undo, Redo, Cut, Copy, Paste, Select All.
//
// Without it the window has no menu bar at all, and on macOS the Command
// key equivalents for editing live in the menu: with no Edit menu there is
// nothing answering Cmd+C or Cmd+V, so copying selected transcript text and
// pasting into the prompt box silently do nothing in the desktop window.
// (Browsers and the Windows window provide these bindings themselves, which
// is why the same page works there.) The items carry no target, so they
// travel the responder chain to whatever is focused, which is the WKWebView
// editor in both cases that matter.
//
// Called once per launch. A second call finds the Edit menu already there
// and returns without adding another.
static void lcInstallEditMenu(void) {
  NSApplication *app = [NSApplication sharedApplication];
  NSMenu *main = [app mainMenu];
  if (main == nil) {
    main = [[[NSMenu alloc] init] autorelease];
    [app setMainMenu:main];
  }
  for (NSMenuItem *item in [main itemArray]) {
    if ([[item title] isEqualToString:@"Edit"]) {
      return;
    }
  }
  NSMenuItem *editItem = [[[NSMenuItem alloc] init] autorelease];
  [editItem setTitle:@"Edit"];
  NSMenu *edit = [[[NSMenu alloc] initWithTitle:@"Edit"] autorelease];
  struct {
    const char *title;
    SEL action;
    const char *key;
    NSEventModifierFlags mask;
  } entries[] = {
    {"Undo", @selector(undo:), "z", NSEventModifierFlagCommand},
    {"Redo", @selector(redo:), "z", NSEventModifierFlagCommand | NSEventModifierFlagShift},
    {NULL, NULL, NULL, 0},
    {"Cut", @selector(cut:), "x", NSEventModifierFlagCommand},
    {"Copy", @selector(copy:), "c", NSEventModifierFlagCommand},
    {"Paste", @selector(paste:), "v", NSEventModifierFlagCommand},
    {"Select All", @selector(selectAll:), "a", NSEventModifierFlagCommand},
  };
  for (unsigned i = 0; i < sizeof(entries) / sizeof(entries[0]); i++) {
    if (entries[i].title == NULL) {
      [edit addItem:[NSMenuItem separatorItem]];
      continue;
    }
    NSString *title = [NSString stringWithUTF8String:entries[i].title];
    NSString *key = [NSString stringWithUTF8String:entries[i].key];
    NSMenuItem *item = [[[NSMenuItem alloc] initWithTitle:title
                                                   action:entries[i].action
                                            keyEquivalent:key] autorelease];
    [item setKeyEquivalentModifierMask:entries[i].mask];
    [edit addItem:item];
  }
  [editItem setSubmenu:edit];
  [main addItem:editItem];
}
*/
import "C"

// installEditMenu gives the macOS window its Edit menu. See the C comment
// above for why the window needs one.
func installEditMenu() {
	C.lcInstallEditMenu()
}
