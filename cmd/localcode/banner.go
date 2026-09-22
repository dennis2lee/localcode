package main

import "fmt"

// logo is a small star-scattered "localcode" wordmark, printed once on
// stdout before the interactive TUI takes the screen (plain text/basic
// Unicode only, no ANSI color — some Windows terminals still mishandle
// raw escape codes, and a startup banner isn't worth the portability
// risk).
const logoTop = `
    ˚    ✦      ˚        ✦
  ✦   l o c a l c o d e    ˚
    ˚      ✦   `

// printBanner shows the logo and the tagline before an interactive
// session starts (the default embedded daemon+TUI, and --server-attached
// TUI-only mode) — --headless skips it since that's meant to run
// unattended, where a big banner in a log file is just noise.
//
// No version. The number printed here is this binary's, and this binary
// is not always the one that ends up running: where it cannot replace
// itself it hands over to a newer copy a moment later, and the banner
// then names a version nobody is using. It said v0.133.0 over a session
// served by v0.141.0 for as long as somebody had an MSI install, which
// is the whole of what a banner version is for and it was getting it
// wrong. "/version" asks the daemon, which is the process that answers
// for what is running; "localcode version" and "--version" answer for
// the binary itself.
func printBanner() {
	fmt.Print(logoTop)
	fmt.Print("Multi LLM coding agent\n\n")
}

// printStartupStep says what the daemon is doing while the terminal has
// nothing else to show.
//
// Building one takes as long as its slowest part, and the parts are not
// alike: reading a config is instant, and one MCP server that does not
// answer outlasts everything else put together. Until now the terminal
// printed the banner and then sat there for however long that took, with
// no way to tell a slow start from a stuck one. The window has had a
// splash saying these same steps since v0.88.0; this is the terminal's
// half of it, arriving late.
//
// Plain lines and no cursor tricks, for the reason the banner gives: a
// startup message is not worth a portability risk. They scroll off when
// the TUI takes the screen.
func printStartupStep(step string) {
	fmt.Printf("  %s\n", step)
}
