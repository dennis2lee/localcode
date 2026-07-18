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

// printBanner shows the logo plus version/tagline before an interactive
// session starts (the default embedded daemon+TUI, and --server-attached
// TUI-only mode) — --headless skips it since that's meant to run
// unattended, where a big banner in a log file is just noise.
func printBanner() {
	fmt.Print(logoTop)
	fmt.Printf("Local & cloud LLM coding agent · v%s\n\n", version)
}
