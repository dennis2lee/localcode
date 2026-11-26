package sshterm

import "strings"

// LocaleEnv is the locale variables of environ, as OpenSSH's default
// SendEnv picks them: LANG and every LC_*.
func LocaleEnv(environ []string) []string {
	var out []string
	for _, kv := range environ {
		k, v, ok := strings.Cut(kv, "=")
		if !ok || v == "" {
			continue
		}
		if k == "LANG" || strings.HasPrefix(k, "LC_") {
			out = append(out, k+"="+v)
		}
	}
	return out
}
