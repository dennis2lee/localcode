package main

import "fmt"

// logo is a block-letter "LOCALCODE" wordmark, opencode-style, printed once
// on stdout before the interactive TUI takes the screen (plain text, no
// ANSI color — some Windows terminals still mishandle raw escape codes,
// and a startup banner isn't worth the portability risk).
const logo = `
█░░░░ ░███░ ░████ ░███░ █░░░░ ░████ ░███░ ████░ █████
█░░░░ █░░░█ █░░░░ █░░░█ █░░░░ █░░░░ █░░░█ █░░░█ █░░░░
█░░░░ █░░░█ █░░░░ █████ █░░░░ █░░░░ █░░░█ █░░░█ ████░
█░░░░ █░░░█ █░░░░ █░░░█ █░░░░ █░░░░ █░░░█ █░░░█ █░░░░
█████ ░███░ ░████ █░░░█ █████ ░████ ░███░ ████░ █████
`

// printBanner shows the logo plus version/tagline before an interactive
// session starts (the default embedded daemon+TUI, and --server-attached
// TUI-only mode) — --headless skips it since that's meant to run
// unattended, where a big banner in a log file is just noise.
func printBanner() {
	fmt.Print(logo)
	fmt.Printf("  로컬/클라우드 LLM 코딩 에이전트 · v%s\n\n", version)
}
