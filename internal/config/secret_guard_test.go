package config

import "testing"

func smartOn(c *Config) *Config {
	on := true
	c.SmartAgent = &on
	return c
}

// The threat is the ordinary one and does not need a malicious model: a
// summarised repository, a "what is in this directory", a stack trace with
// a path in it. Once a credential is in a context it has been sent to a
// provider, and there is no taking it back.
func TestSecretsAreRefusedWithSmartAgentOn(t *testing.T) {
	cfg := smartOn(&Config{})
	for _, path := range []string{
		".env", ".env.local", "app/.env", "/home/u/project/.env.production",
		"/home/u/.ssh/id_rsa", "certs/server.pem", "keys/private.key",
		"/home/u/.aws/credentials", "/home/u/.kube/config",
		"service-account-prod.json", "/home/u/.npmrc",
	} {
		for _, tool := range []string{"read_file", "write_file", "edit"} {
			if got := cfg.ResolvePermission(tool, path, false); got != DecisionDeny {
				t.Errorf("%s %s = %q, want deny", tool, path, got)
			}
		}
	}
}

func TestOrdinaryFilesAreUnaffected(t *testing.T) {
	cfg := smartOn(&Config{})
	for _, path := range []string{
		"main.go", "src/app/env.ts", "docs/environment.md",
		"/home/u/project/keyboard.go", "internal/keys/registry.go",
	} {
		if got := cfg.ResolvePermission("read_file", path, false); got != DecisionAllow {
			t.Errorf("read_file %s = %q, want allow", path, got)
		}
	}
}

// Off is off: the guard is part of the Smart Agent bundle, not a change to
// what every existing install does.
func TestSecretsAreNotGuardedWithSmartAgentOff(t *testing.T) {
	cfg := &Config{}
	if got := cfg.ResolvePermission("read_file", ".env", false); got != DecisionAllow {
		t.Errorf("read_file .env = %q with the feature off, want the previous behaviour", got)
	}
}

// Somebody who genuinely needs the agent to edit a .env says so, and is
// obeyed. A guard that cannot be turned off is a guard people work around.
func TestAnExplicitRuleOverridesTheGuard(t *testing.T) {
	cfg := smartOn(&Config{
		Permissions: map[string]ToolPermission{
			"read_file": {Rules: []PermissionRule{{Match: "*.env", Decision: DecisionAllow}}},
		},
	})
	if got := cfg.ResolvePermission("read_file", ".env", false); got != DecisionAllow {
		t.Errorf("read_file .env = %q, want the user's own rule to win", got)
	}
	// And only for what the rule covers.
	if got := cfg.ResolvePermission("read_file", "/home/u/.ssh/id_rsa", false); got != DecisionDeny {
		t.Errorf("read_file id_rsa = %q, want the guard to still apply", got)
	}
}

// skip_permissions downgrades ask to allow and never touches deny. A
// convenience switch must not quietly unlock the one category that was
// refused rather than questioned.
func TestSkippingPermissionsDoesNotUnlockSecrets(t *testing.T) {
	skip := true
	cfg := smartOn(&Config{SkipPermissions: &skip})
	if got := cfg.ResolvePermission("read_file", "/home/u/.ssh/id_rsa", false); got != DecisionDeny {
		t.Errorf("read_file id_rsa = %q with skip_permissions on, want deny", got)
	}
}

// A shell command is not a path, and pattern-matching "cat .env" out of an
// arbitrary command line catches the honest case and misses every other
// one. The shell has its own rules; pretending otherwise would be a guard
// that reads as protection and is not.
func TestTheShellIsNotGuardedByPathPatterns(t *testing.T) {
	cfg := smartOn(&Config{})
	// Not a deny, and not claimed to be one. bash keeps asking, which is
	// what it did before.
	if got := cfg.ResolvePermission(BashToolName, "cat .env", true); got == DecisionDeny {
		t.Error("bash was denied by a path pattern, which is a guard that does not hold")
	}
}
