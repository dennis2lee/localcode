package config

import (
	"strings"
	"testing"
)

// The two permission tables walked entry by entry.
//
// Both were spot-checked by hand-written lists that named a few members,
// and both were wrong in a way the spot checks could not see: the secret
// patterns denied only the absolute spelling of seven credential files,
// and the wide-program table missed every ".exe" name but the two that
// happened to be listed twice. A hand-written sample proves the members
// it names. Only a walk proves the list.

// The credential files this build refuses to touch, named as files rather
// than as patterns, and checked in both spellings.
//
// Stated independently of secretPatterns on purpose. The list is the
// implementation and this is the requirement: keying the test on the
// patterns is what let the hole survive, because the one test path that
// touched "*/.npmrc" was the absolute spelling the pattern happened to
// match. A change to how the list spells something has to fail here as
// "this file is readable", not as "the test needs updating".
//
// The relative spelling is the one that matters. The subject arrives as
// the model wrote it (see secretPatterns), and a model reading a file in
// the directory it is working in writes ".npmrc", not "/home/u/.npmrc".
var mustNeverBeTouched = []string{
	".env", ".env.local", ".env.production",
	"id_rsa", "id_ed25519", "id_ecdsa", "id_dsa",
	"server.pem", "private.key", "bundle.p12", "cert.pfx", "release.keystore",
	".ssh/known_hosts", ".ssh/id_rsa", ".ssh/config",
	".aws/credentials", ".aws/config",
	".gnupg/secring.gpg", ".kube/config", ".docker/config.json",
	"credentials.json", ".netrc", ".htpasswd", ".npmrc", ".pypirc",
	"service-account-key.json",
}

func TestEveryCredentialFileIsDeniedInBothSpellings(t *testing.T) {
	cfg := smartOn(&Config{})
	for _, file := range mustNeverBeTouched {
		for _, subject := range []string{file, "/home/someone/" + file, "~/" + file} {
			for _, tool := range []string{"read_file", "write_file", "edit"} {
				if got := cfg.ResolvePermission(tool, subject, false); got != DecisionDeny {
					t.Errorf("%s %s = %q, want deny", tool, subject, got)
				}
			}
		}
	}
}

// And no pattern in the list is dead: each one is the only reason some
// path is denied, or it is doing nothing.
//
// Separate from the test above because the two catch different mistakes.
// That one catches a file falling through the list; this one catches a
// pattern that can never match anything — a typo, or a spelling like
// "*/.npmrc" whose literal "/" the subject never carries.
func TestNoSecretPatternIsDead(t *testing.T) {
	for _, pattern := range secretPatterns {
		var matched int
		for _, file := range mustNeverBeTouched {
			for _, subject := range []string{file, "/home/someone/" + file, "~/" + file} {
				if globMatch(pattern, strings.ToLower(subject)) {
					matched++
				}
			}
		}
		if matched == 0 {
			t.Errorf("pattern %q matches none of the %d credential files this build guards, in any spelling — it is doing nothing",
				pattern, len(mustNeverBeTouched))
		}
	}
}

// Every name in the wide-program table is spelled the way the lookup
// normalizes, and every one of them keeps the whole command rather than
// generalizing to "<program> *".
//
// The ".exe" half is the reason this exists: the lookup trims it now, and
// a key carrying ".exe" would be unreachable — which is what
// "powershell.exe" and "cmd.exe" were, listed twice to work around a trim
// that was not happening.
func TestEveryWideProgramKeepsItsWholeCommand(t *testing.T) {
	if len(wideProgramNames) < 40 {
		t.Fatalf("only %d wide program names — the list is not being read", len(wideProgramNames))
	}
	for name := range wideProgramNames {
		if name != normalizeProgram(name) {
			t.Errorf("%q is not how the lookup spells it (%q), so nothing can ever match it",
				name, normalizeProgram(name))
		}
		// Every spelling a person's shell produces reaches the same answer.
		for _, program := range []string{
			name,
			strings.ToUpper(name),
			name + ".exe",
			"/usr/bin/" + name,
			`C:\tools\` + name + ".exe",
		} {
			command := program + " something --flag"
			rule := PermissionRuleFor(BashToolName, command)
			if rule.Match != command {
				t.Errorf("always-allow for %q persists %q, want the whole command — %q generalizes to a wildcard",
					command, rule.Match, program)
			}
		}
	}
}

// And the generalization still happens for a program that is not on the
// list, which is the behaviour the table is an exception to.
func TestAnOrdinaryProgramStillGeneralizes(t *testing.T) {
	for _, program := range []string{"go", "git", "make", "cargo", "go.exe"} {
		rule := PermissionRuleFor(BashToolName, program+" build ./...")
		want := program + " *"
		if rule.Match != want {
			t.Errorf("always-allow for %q persists %q, want %q", program+" build ./...", rule.Match, want)
		}
	}
}
