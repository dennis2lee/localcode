package main

import (
	"fmt"
	"strings"
)

// parseScope pulls -s/--scope out of args and returns the remaining
// positional arguments. Used by the mcp subcommands whose only flag is
// --scope (add-json, remove); `mcp add` has its own parser since it also
// takes --env/--header/--transport and a trailing "-- <command>", and
// `mcp import-claude` has its own since it takes a boolean --skip-existing
// and accepts no positionals at all.
//
// Unknown flags are rejected here — before this helper existed, add-json
// and remove silently treated an unrecognized flag like "--bogus" as a
// positional argument instead of erroring, while `mcp add` already
// rejected it. This is what makes the two agree with `mcp add` instead of
// each other.
func parseScope(args []string) (scope string, rest []string, err error) {
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "-s" || a == "--scope":
			if i+1 >= len(args) {
				return "", nil, fmt.Errorf("--scope requires an argument (global|project)")
			}
			scope = args[i+1]
			i++
		case a != "-" && strings.HasPrefix(a, "-"):
			return "", nil, fmt.Errorf("unknown flag %q", a)
		default:
			rest = append(rest, a)
		}
	}
	return scope, rest, nil
}
