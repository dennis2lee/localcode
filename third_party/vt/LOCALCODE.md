# vt, as localcode carries it

A copy of `github.com/charmbracelet/x/vt` v0.1.0 (MIT, see LICENSE), used
through a `replace` directive in localcode's go.mod, with one change.

## The change

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
The upstream main branch (2026-11-22) has the same code; when a release
fixes it, drop this copy and the replace directive.
