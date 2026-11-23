package sshterm

import (
	"path"
	"regexp"
	"strings"
	"time"
)

// Shell integration: knowing when a command ends.
//
// localcode types one line into the shell, once, after the first prompt.
// It has bash and zsh print an invisible mark (OSC 133;D;status, the
// FinalTerm "command finished" mark) before every prompt. The line is
// shown on the screen as anything typed is. It starts with a space, which
// keeps it out of the history where HISTCONTROL or HIST_IGNORE_SPACE ask
// for that.
//
// One line for both shells, deciding by itself which it is in: a login
// shell of bash whose startup file starts zsh is zsh by the time the line
// arrives. It is typed only after a quiet first prompt that looks like a
// shell prompt, and only when the login shell (read on a separate
// channel) is bash or zsh, or, when the server will not say, only after
// a "$" or "#" prompt. Any other shell (fish, csh, a Windows shell) would
// print an error for it, and gets no integration.
//
// What it does, in the shell:
//   - __lc_p, first in PROMPT_COMMAND (bash) or precmd_functions (zsh),
//     prints the D mark with the last command's status, and returns that
//     status, so a prompt command after it still sees it.
//   - __lc_c prints the C mark as a command starts: from PS0 in bash 4.4
//     and later, from preexec_functions in zsh. Without it (bash 3.2), the
//     output of a command is what follows the first line break.
//   - Every mark carries the terminal's nonce, so output that merely
//     contains a mark is not taken for one.
//   - It adds itself once, survives set -u and zsh's NO_UNSET.

// integrationLine is the line for nonce.
func integrationLine(nonce string) string {
	return ` __lc_n=` + nonce + `; __lc_p(){ local s=$?; printf '\033]133;D;%s;lc=%s\007' "$s" "$__lc_n"; return $s; }; __lc_c(){ printf '\033]133;C;lc=%s\007' "$__lc_n"; }; if [ -n "${ZSH_VERSION-}" ]; then typeset -ga precmd_functions preexec_functions; precmd_functions=(__lc_p ${precmd_functions:#__lc_p}); preexec_functions=(__lc_c ${preexec_functions:#__lc_c}); else case ";${PROMPT_COMMAND-};" in *";__lc_p;"*) ;; *) PROMPT_COMMAND="__lc_p${PROMPT_COMMAND:+;$PROMPT_COMMAND}";; esac; PS0="$(__lc_c)${PS0-}"; fi`
}

var shellPath = regexp.MustCompile(`^/[^\s]*$`)

// integrate waits for the first prompt, then types the integration line
// when the login shell is one it works in.
func (t *Terminal) integrate() {
	// The first prompt: output, then quiet for a moment.
	start := time.Now()
	for time.Since(start) < 5*time.Second {
		t.mu.Lock()
		state, seen, quiet, already := t.state, !t.lastOutput.IsZero(), time.Since(t.lastOutput), t.integrated
		t.mu.Unlock()
		if state != StateConnected || already {
			return
		}
		if seen && quiet > 300*time.Millisecond {
			break
		}
		select {
		case <-t.ctx.Done():
			return
		case <-time.After(100 * time.Millisecond):
		}
	}
	name := t.loginShell()
	t.mu.Lock()
	t.shell = name
	line := strings.TrimRight(t.cursorLine(), " ")
	typeIt := false
	switch {
	case name == "bash" || name == "zsh":
		typeIt = looksLikePrompt(line)
	case name == "":
		typeIt = strings.HasSuffix(line, "$") || strings.HasSuffix(line, "#")
	}
	if askingSecret.MatchString(line+" ") || strings.HasSuffix(line, "?") || strings.HasSuffix(line, ":") {
		// A question on the first screen (an expired password, a menu):
		// the line would be its answer.
		typeIt = false
	}
	if !typeIt || t.state != StateConnected || t.integrated || t.personTyped || !t.lastPerson.IsZero() {
		// Not a shell this line is for, or the person got there first:
		// their line is not ours to type into.
		t.notify()
		t.mu.Unlock()
		return
	}
	t.running = true
	t.runStarted = time.Now()
	t.integrating = true
	t.emu.SendText(integrationLine(t.marks.nonce) + "\r")
	t.notify()
	t.mu.Unlock()
	defer func() {
		t.mu.Lock()
		t.integrating = false
		t.notify()
		t.mu.Unlock()
	}()
	// The first mark turns integration on (see output). Without one the
	// state goes back to "not known".
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		t.mu.Lock()
		on := t.integrated
		t.mu.Unlock()
		if on {
			return
		}
		select {
		case <-t.ctx.Done():
			return
		case <-time.After(100 * time.Millisecond):
		}
	}
	t.mu.Lock()
	if !t.integrated {
		t.running = false
		t.notify()
	}
	t.mu.Unlock()
}

// loginShell asks the server for the login shell's name, on a channel of
// its own, and returns "" when it cannot tell.
func (t *Terminal) loginShell() string {
	t.mu.Lock()
	client := t.client
	t.mu.Unlock()
	if client == nil {
		return ""
	}
	sess, err := client.NewSession()
	if err != nil {
		return ""
	}
	defer sess.Close()
	type result struct {
		out []byte
		err error
	}
	ch := make(chan result, 1)
	go func() {
		out, err := sess.Output(`printf '%s' "$SHELL"`)
		ch <- result{out, err}
	}()
	select {
	case r := <-ch:
		if r.err != nil {
			return ""
		}
		s := strings.TrimSpace(string(r.out))
		if !shellPath.MatchString(s) {
			return ""
		}
		return path.Base(s)
	case <-time.After(5 * time.Second):
		return ""
	case <-t.ctx.Done():
		return ""
	}
}
