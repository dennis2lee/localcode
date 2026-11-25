# vt, as localcode carries it

A copy of `github.com/charmbracelet/x/vt` v0.1.0 (MIT, see LICENSE), used
through a `replace` directive in localcode's go.mod, with the changes
below. Every changed line carries a `localcode:` comment.

## Wide characters at the margin

`utf8.go`, `handleGrapheme`: wrapping around a character two columns wide.

* A wide character that ends in the last column leaves the cursor on the
  last column with a wrap pending, as a narrow character in the last column
  does. v0.1.0 judged the pending wrap by where the character starts, so the
  next character was written over the wide one's second half instead of
  wrapping.
* A wide character that does not fit in what is left of the line is
  written at the start of the next line, as xterm does. v0.1.0 dropped it.

Seen as: Korean typed into zsh at the right margin. zsh ends a line that
fills the width exactly with a space and a carriage return, then erases to
the end of the line; with no wrap pending, that erased the line it had
just drawn.

The test is `TestWideCharactersWrapAtTheMargin` in `utf8_localcode_test.go`.

## A cursor past the edge after the screen shrank

`screen.go` (`RestoreCursor`, `clampCursors`), `emulator.go` (`Resize`),
`cc.go` (`horizontalTabSet`), `handlers.go` (TBC): a cursor saved at a
column the screen no longer has (saved, then the terminal narrowed, then
restored, or a full-screen program opened wide and quit narrow) was
restored past the last column, and a tab stop set or cleared there indexed
past the tab-stop table and panicked. In localcode the screen is fed from
a goroutine, so remote output could end the program. Saved and current
cursors of both screens are now kept inside the screen on a resize and a
restore, and setting or clearing a tab stop checks the column.

## Margins past the screen

`handlers.go` (DECSTBM `r`, DECSLRM `s`) and `emulator.go` (`Write`): a
bottom or right margin past the screen was taken as given, so scrolling
indexed past the last row and panicked, and the cursor could be left off
the screen. Margins are cut to the screen (a region that starts on it is
kept, as the upstream tests expect), and every write ends with both
screens' cursors inside them.

## Counts with no bound

`csi_cursor.go` (REP), `handlers.go` (SU, SD): REP repeated the last
character as many times as asked, so `a ESC[99999999b` held the emulator
for seconds; it now repeats at most a screen's worth. SU and SD scroll at
most the screen's height.

The tests are in `localcode_test.go`. The upstream main branch
(2026-11-22) has the same code for all of the faults above; when a release
fixes them, the copy is still needed for the addition below, until
upstream can be asked the same question.

## Whether a paste is bracketed

`emulator.go` (`BracketedPaste`): an accessor for the mode `Paste` already
goes by. To a shell that has not turned bracketed paste on (bash 3.2), a
pasted line end sends the line, and localcode keeps track of whether the
command line was sent. v0.1.0 has no way to ask. This one is an addition,
not a fix: dropping this copy needs the same question answered another
way.

## Whether a wrap is pending

`emulator.go` (`PendingWrap`): an accessor for the state a character
written in the last column leaves (`atPhantom`). The cursor is then
reported on that character, and a cursor in the last column over a blank
cell is reported the same way. localcode reads the text left of the cursor
to find what the person typed, and has to know whether the cell under it
is part of that text. v0.1.0 has no way to ask. This one is an addition,
not a fix.