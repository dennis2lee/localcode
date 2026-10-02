package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"localcode/internal/commands"
	"localcode/internal/config"
	"localcode/internal/events"
	"localcode/internal/memory"
	"localcode/internal/provider"
	"localcode/internal/skills"
	"localcode/internal/tools"
)

// routeSkillCommand recognizes "/skill" and "/skill <name> [args]" — the
// older spelling of running a skill, kept working so it doesn't break
// under anyone's fingers; "/<name>" (routeSkillName) is the documented one.
func (l *Loop) routeSkillCommand(ctx context.Context, sessionID, agentName, text string) (bool, error) {
	arg, ok := parseSkillCommand(text)
	if !ok {
		return false, nil
	}
	if arg == "" {
		return true, l.listSkills(sessionID, text)
	}
	name, args := arg, ""
	if idx := strings.IndexAny(arg, " \t"); idx >= 0 {
		name, args = arg[:idx], strings.TrimSpace(arg[idx+1:])
	}
	// A registered name always wins: the ambiguity between a name and
	// a file of the same spelling resolves the safe way, toward the
	// skill that was installed and listed rather than an arbitrary
	// file that happens to read the same.
	if sk, found := l.findSkill(name); found {
		skillText, skillSpans := skillModelText(sk, args)
		return true, l.sendWithModelText(ctx, sessionID, agentName, text, skillText, "", "",
			messageOrigin{source: "skill.frame." + sk.Name, spans: skillSpans})
	}
	// Otherwise a path-shaped argument names a file to run for this
	// turn. The shape follows looksLikeCommand: a second slash, a
	// backslash, a dot, or a leading ~ reads as a path, so a bare word
	// stays a name and keeps the unknown-skill answer below.
	if !looksLikeSkillPath(name) {
		l.Store.Append(sessionID, events.TypeUserMessage, map[string]any{"text": text, "local": true})
		l.Store.Append(sessionID, events.TypeError, map[string]any{
			"error": fmt.Sprintf("unknown skill %q. Available: %s", name, l.skillNames()),
		})
		return true, nil
	}
	return true, l.runSkillPath(ctx, sessionID, agentName, text, name, args)
}

// looksLikeSkillPath reports whether a /skill argument that matched no
// registered skill reads as a path. The same shape the unknown-command
// route uses: a second slash or a dot is what a path looks like, so
// "/etc/hosts" is prose about a file and "/clean" is a command. A
// leading ~ and a backslash are paths for the same reason on their own
// platforms. A bare word is not: without a slash or a dot there is no
// telling it from a skill name, and it keeps the unknown-skill answer.
func looksLikeSkillPath(name string) bool {
	if strings.HasPrefix(name, "~") {
		return true
	}
	return strings.ContainsAny(name, "/\\.")
}

// resolveSkillPathArg turns what was typed after /skill into the file it
// names. A leading ~ expands to the home directory; a relative path
// resolves against the session's workspace, the same claim a tool makes
// when it takes a relative path.
func resolveSkillPathArg(raw, workspace string) string {
	goos := runtime.GOOS
	resolved := resolveSkillPathFor(goos, raw, workspace, skillHomeDir(goos))
	// Normalised on the way out rather than inside the resolver: cleaning
	// is the host's job, since the host is where the path is about to be
	// opened, and keeping it out of the resolver is what lets the resolver
	// answer for a platform this machine is not.
	return filepath.Clean(resolved)
}

// skillHomeDir is os.UserHomeDir with the platform handed to it: the
// home variable is HOME everywhere but Windows, where it is USERPROFILE
// (os/file.go UserHomeDir). Reading it through runtime.GOOS inside the
// resolver hid the Windows branch from every test run elsewhere, which
// is how "~ did not expand to home" reached CI.
func skillHomeDir(goos string) string {
	if goos == "windows" {
		return os.Getenv("USERPROFILE")
	}
	return os.Getenv("HOME")
}

// isAbsSkillPath is filepath.IsAbs with the platform handed to it. On
// Windows a drive-letter path with a rooted remainder (C:\work) and a
// UNC path (\\host\share\work) are absolute, and a merely rooted path
// (\work) is not, which filepath.IsAbs on Unix never says.
func isAbsSkillPath(goos, p string) bool {
	if goos != "windows" {
		return strings.HasPrefix(p, "/")
	}
	if len(p) >= 3 && p[1] == ':' && (p[2] == '\\' || p[2] == '/') {
		return true
	}
	if len(p) > 2 && ((p[0] == '\\' && p[1] == '\\') || (p[0] == '/' && p[1] == '/')) {
		return true
	}
	return false
}

// resolveSkillPathFor is resolveSkillPathArg with the platform and the
// home directory handed to it, so tests can drive the Windows branch
// from any machine. Joining and cleaning still use the host filepath:
// the platform-dependent decisions are which variable names home and
// what counts as absolute, not how separators spell.
func resolveSkillPathFor(goos, raw, workspace, home string) string {
	p := strings.TrimSpace(raw)
	if p == "~" || strings.HasPrefix(p, "~/") {
		if home != "" {
			p = joinSkillPath(goos, home, strings.TrimPrefix(p, "~"))
		}
	}
	if isAbsSkillPath(goos, p) {
		return p
	}
	if workspace != "" {
		return joinSkillPath(goos, workspace, p)
	}
	return p
}

// skillSeparator is the path separator of the platform being answered
// for. Handed in rather than read from the host for the same reason the
// rest of this is: a separator taken from runtime.GOOS spells every
// answer in the running machine's alphabet, so a test driving the
// Windows branch from a Mac gets slashes back and proves nothing.
func skillSeparator(goos string) string {
	if goos == "windows" {
		return `\`
	}
	return "/"
}

// joinSkillPath joins two path pieces in the spelling of goos. Only the
// separator differs, which is all this needs: the caller has already
// decided which piece is a base and which is a remainder.
func joinSkillPath(goos, base, rest string) string {
	sep := skillSeparator(goos)
	base = strings.TrimRight(base, `/\`)
	rest = strings.TrimLeft(rest, `/\`)
	switch {
	case base == "":
		return rest
	case rest == "":
		return base
	}
	return base + sep + rest
}

// runSkillPath reads the file name points at, parses it as a skill, and
// runs it exactly as a registered skill runs — same model text, same
// skill.frame origin — without registering it. The next turn does not
// have it, /skill with no argument does not list it, and completion
// does not offer it, because the registry is never touched.
//
// A file outside the session's workspace goes through the same
// outside-read boundary read_file does: the read is gated through the
// tool registry's read_file call, so the read_outside switch, the
// broker's ask, the remembered directories, and the unattended refusal
// all apply unchanged. The question names the file and says its
// contents will be given to the model as instructions, because the
// consequence is different from reading a file into a tool result.
//
// Without a registry there is nobody to ask, so the fallback refuses
// what it cannot ask about: a file outside the workspace is refused,
// the way the unattended path refuses, and the refusal says so. A file
// inside the workspace runs without a question — it is a file in the
// project. The inside-vs-outside question is the boundary's own
// OutsideWorkspace, not a second containment check.
func (l *Loop) runSkillPath(ctx context.Context, sessionID, agentName, displayText, rawPath, args string) error {
	resolved := resolveSkillPathArg(rawPath, l.SessionDir(sessionID))
	fail := func(format string, fargs ...any) error {
		l.Store.Append(sessionID, events.TypeUserMessage, map[string]any{"text": displayText, "local": true})
		l.Store.Append(sessionID, events.TypeError, map[string]any{"error": fmt.Sprintf(format, fargs...)})
		return nil
	}
	available := func() string { return l.skillNames() }

	if l.Tools != nil {
		// The agent this is being run for, pinned the way a turn pins it,
		// because the read below is a permission decision and a decision
		// taken without it is taken against the top-level rules alone.
		// A skill run under an agent whose own permission block denies
		// read_file has to be denied here too, and one whose block allows
		// it has to be allowed — neither happened while this call was the
		// only permission path in the program that did not say whose work
		// it was.
		gate := tools.WithWorkingDir(
			config.WithAgent(WithSessionID(ctx, sessionID), agentName),
			l.SessionDir(sessionID))
		input, _ := json.Marshal(map[string]string{"path": resolved})
		res := l.Tools.Call(gate, "read_file", input,
			fmt.Sprintf("run skill file %q: its contents will be given to the model as instructions for this turn", resolved))
		if res.Refused {
			return fail("cannot run skill file %q: %s", rawPath, res.Content)
		}
		if res.IsError {
			// The gate reads before we do, so a directory arrives here
			// as the gate's read error. On Unix that error says "is a
			// directory"; on Windows the OS says "Incorrect function."
			// Stat after the gate (never before: a refused path must
			// answer "denied", not confirm it is a directory) and say
			// our own sentence on both platforms.
			if fi, err := os.Stat(resolved); err == nil && fi.IsDir() {
				return fail("cannot run skill file %q: it is a directory, not a file. Available: %s", rawPath, available())
			}
			return fail("cannot run skill file %q: %s. Available: %s", rawPath, res.Content, available())
		}
	} else {
		// Nothing to ask with, so nothing runs. Production always wires
		// a registry (see agent.New), which is the point: a gate that
		// can be stepped around by a Loop assembled differently is not a
		// gate. Refusing here costs nothing real and keeps the rule one
		// sentence long — a file only becomes instructions through the
		// boundary that asks about it.
		return fail("cannot run skill file %q: there is no tool registry, so there is nothing to ask permission with", rawPath)
	}

	if fi, err := os.Stat(resolved); err != nil {
		return fail("cannot run skill file %q: %v. Available: %s", rawPath, err, available())
	} else if fi.IsDir() {
		return fail("cannot run skill file %q: it is a directory, not a file. Available: %s", rawPath, available())
	}
	// Read raw rather than running the gate's result. The tool's result
	// is not the file: with Smart Agent on it can come back windowed and
	// annotated ("[lines 1-800 of 2000 ...]"), and running a truncated
	// skill body as instructions would be worse than not running it. The
	// gate above is permission only; this read is what gets parsed.
	data, err := os.ReadFile(resolved)
	if err != nil {
		return fail("cannot run skill file %q: %v. Available: %s", rawPath, err, available())
	}
	sk, err := skills.ParseContent(resolved, string(data))
	if err != nil {
		return fail("cannot run skill file %q: %v. Available: %s", rawPath, err, available())
	}
	if strings.TrimSpace(sk.Body) == "" {
		return fail("cannot run skill file %q: it has no usable body. Available: %s", rawPath, available())
	}
	// The path is the identity: frontmatter name and description stay
	// advisory, and the quoted path in the model text is what makes
	// clear which file ran.
	sk.Name = resolved
	sk.Path = resolved
	skillText, skillSpans := skillModelText(sk, args)
	return l.sendWithModelText(ctx, sessionID, agentName, displayText, skillText, "", "",
		messageOrigin{source: "skill.frame." + sk.Name, spans: skillSpans})
}

// parseSkillCommand recognizes "/skill" and "/skill <name>". ok is false
// for anything else (including a message that merely mentions "/skill" in
// the middle of a sentence).
func parseSkillCommand(text string) (arg string, ok bool) {
	trimmed := strings.TrimSpace(text)
	if trimmed == "/skill" {
		return "", true
	}
	if rest, found := strings.CutPrefix(trimmed, "/skill "); found {
		return strings.TrimSpace(rest), true
	}
	return "", false
}

// listSkills answers "/skill" locally — no model call — with the same
// name/description index that's in the system prompt.
func (l *Loop) listSkills(sessionID, displayText string) error {
	text := "No skills registered."
	if list := l.SkillList(); len(list) > 0 {
		var b strings.Builder
		b.WriteString("Available skills (/<name> to run one):\n")
		for _, s := range list {
			fmt.Fprintf(&b, "- %s: %s\n", s.Name, s.Description)
		}
		text = b.String()
	}
	return l.replyLocal(sessionID, displayText, text)
}

// showMemoryInfo answers "/memory" locally — no model call — with the
// auto-memory directory path and current MEMORY.md index content, the
// same information Claude Code's "/memory" command surfaces.
func (l *Loop) showMemoryInfo(sessionID, displayText string) error {
	var text string
	if l.MemoryDir == "" {
		text = "Auto memory is disabled (config.json's \"auto_memory_enabled\": false)."
	} else {
		index := memory.LoadIndex(l.MemoryDir)
		var b strings.Builder
		fmt.Fprintf(&b, "Auto memory directory: %s\n", l.MemoryDir)
		fmt.Fprintf(&b, "Index file: %s\n\n", memory.IndexPath(l.MemoryDir))
		if index == "" {
			b.WriteString("No memory saved yet.")
		} else {
			b.WriteString(index)
		}
		text = b.String()
	}
	return l.replyLocal(sessionID, displayText, text)
}

func (l *Loop) findSkill(name string) (skills.Skill, bool) {
	for _, s := range l.SkillList() {
		if strings.EqualFold(s.Name, name) {
			return s, true
		}
	}
	return skills.Skill{}, false
}

func (l *Loop) skillNames() string {
	list := l.SkillList()
	names := make([]string, len(list))
	for i, s := range list {
		names[i] = s.Name
	}
	return strings.Join(names, ", ")
}

// matchSkillName recognizes "/<skill-name>" and "/<skill-name> <args>"
// against a registered skill.
func (l *Loop) matchSkillName(text string) (skills.Skill, string, bool) {
	trimmed := strings.TrimSpace(text)
	rest, ok := strings.CutPrefix(trimmed, "/")
	if !ok {
		return skills.Skill{}, "", false
	}
	name, args := rest, ""
	if idx := strings.IndexAny(rest, " \t"); idx >= 0 {
		name, args = rest[:idx], strings.TrimSpace(rest[idx+1:])
	}
	if name == "" {
		return skills.Skill{}, "", false
	}
	sk, found := l.findSkill(name)
	if !found {
		return skills.Skill{}, "", false
	}
	return sk, args, true
}

// skillModelText builds what the model actually receives when a skill is
// invoked: the skill's whole body, plus whatever the user typed after the
// command name, if anything. The transcript keeps only the short
// "/<name> ..." line the user typed.
// skillModelText returns the text and the spans that say which part of
// it is whose. Three authors in one message: localcode wrote the
// framing, whoever installed the skill wrote the body, and the person
// typed the arguments. Hashing the lot as one skill entry attributed
// all three to the skill.
func skillModelText(sk skills.Skill, args string) (string, []provider.BlockSource) {
	head := fmt.Sprintf("Follow the %q skill's instructions below to help with my request.\n\n---\n", sk.Name)
	var b strings.Builder
	var spans []provider.BlockSource
	b.WriteString(head)
	from := b.Len()
	b.WriteString(sk.Body)
	spans = append(spans, provider.BlockSource{ID: "skill.body." + sk.Name, From: from, To: b.Len()})
	b.WriteString("\n---")
	if args != "" {
		b.WriteString("\n\nMy request: ")
		from = b.Len()
		b.WriteString(args)
		spans = append(spans, provider.BlockSource{ID: "argument." + sk.Name, From: from, To: b.Len()})
	}
	return b.String(), spans
}

// matchCustomCommand recognizes "/<name>" or "/<name> <args>" against a
// loaded custom command. Built-in commands (/skill, /init) are checked by
// the caller first, so they always take precedence over a same-named
// custom command.
func (l *Loop) matchCustomCommand(text string) (commands.Command, string, bool) {
	trimmed := strings.TrimSpace(text)
	rest, ok := strings.CutPrefix(trimmed, "/")
	if !ok {
		return commands.Command{}, "", false
	}
	name, args := rest, ""
	if idx := strings.IndexAny(rest, " \t"); idx >= 0 {
		name, args = rest[:idx], strings.TrimSpace(rest[idx+1:])
	}
	for _, c := range l.Commands {
		if c.Name == name {
			return c, args, true
		}
	}
	return commands.Command{}, "", false
}
