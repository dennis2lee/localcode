package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"localcode/internal/hooks"
)

// DefaultGlobalPath returns ~/.localcode/config.json.
func DefaultGlobalPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve home dir: %w", err)
	}
	return filepath.Join(home, ".localcode", "config.json"), nil
}

// LoadMerged loads the global config, then merges a project-local
// .localcode/config.json on top (project entries win). Either file may be
// absent; at least one must exist. A file either of them lists under
// "include" is read too, laid under the file that lists it.
//
// The second return is what the files said that localcode accepted and did
// not act on — opencode's spellings for things it has no equivalent of, in
// opencode's own dotted paths, so the person can grep their own file for
// the string. A caller with nowhere to print it writes `_`, and there is
// deliberately no second function that drops it for them: a wrapper that
// hides this was the one thing nobody called the moment the daemon started
// printing it.
func LoadMerged(projectDir string) (*Config, []string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, nil, fmt.Errorf("resolve home dir: %w", err)
	}
	return loadMergedFrom(configSources(home, projectDir), home)
}

// loadMergedFrom is LoadMerged with the list of files handed to it, so
// the order can be exercised without a home directory to arrange.
func loadMergedFrom(sources []source, home string) (*Config, []string, error) {
	cfg, notes, setAside, err := loadMergedDetail(sources, home)
	if err != nil {
		return nil, nil, err
	}
	// Two different things, joined here because the caller has one place
	// to print them and the distinction is in the words. A key that was
	// accepted and not acted on is a line about a key; a file that was set
	// aside is a line about a file, and saying "ignored keys: <a whole
	// refusal>" told somebody localcode had read the rest of the file when
	// it had read none of it.
	for _, s := range setAside {
		notes = append(notes, "set aside and not read: "+s)
	}
	return cfg, notes, nil
}

func loadMergedDetail(sources []source, home string) (*Config, []string, []string, error) {
	var cfg *Config
	var notes []string
	var setAside []string
	var listed []string

	for _, src := range sources {
		// Every source here is localcode's own, so a file that cannot be
		// read or holds a key localcode cannot honour stops everything:
		// the person wrote it for localcode, and finding out at startup
		// beats finding out from behaviour. The files it lists are
		// another matter, and loadOne says what happens to those.
		one, oneNotes, aside, read, err := loadOne(src.path, home)
		if err != nil {
			return nil, nil, nil, err
		}
		if one == nil {
			continue
		}
		notes = append(notes, oneNotes...)
		setAside = append(setAside, aside...)
		listed = append(listed, read...)
		if cfg == nil {
			cfg = one
			continue
		}
		cfg.merge(one)
	}

	if cfg == nil {
		var names []string
		for _, src := range sources {
			names = append(names, src.path)
		}
		return nil, nil, nil, fmt.Errorf("no config found at any of: %s. A file written for opencode or Claude Code is read only when a config.json lists it under \"include\"", strings.Join(names, ", "))
	}
	if err := cfg.Validate(); err != nil {
		return nil, nil, nil, fmt.Errorf("invalid merged config: %w%s", err, listedFiles(listed))
	}

	seen := make(map[string]bool)
	out := notes[:0]
	for _, n := range notes {
		if !seen[n] {
			seen[n] = true
			out = append(out, n)
		}
	}
	return cfg, out, setAside, nil
}

// loadOne reads the file at path and every file it lists under "include",
// and returns them as one Config with the listed files laid under the one
// that names them, in the order listed. A nil Config means path does not
// exist.
//
// An error is for path itself. A listed file that is missing, cannot be
// parsed, or holds a key localcode cannot honour comes back in the third
// return as a sentence about that file, and the rest is read without it:
// the list is the person's, but the files on it were written for another
// program, and another program's file is not a reason this one cannot
// start. The second return is the notes from all of them, and the fourth is
// the listed files that were read, in the order they were laid.
func loadOne(path, home string) (*Config, []string, []string, []string, error) {
	one, oneNotes, err := loadOptional(path)
	if err != nil || one == nil {
		return nil, nil, nil, nil, err
	}
	layers, notes, setAside, read := loadIncluded(one, path, home)

	var out *Config
	for _, c := range layers {
		if out == nil {
			out = c
			continue
		}
		out.merge(c)
	}
	if out == nil {
		out = one
	} else {
		out.merge(one)
	}
	// Consumed. A merged Config says what the files said, and the list of
	// files it was read from is not something the files said to be run.
	out.Include = nil
	return out, append(notes, oneNotes...), setAside, read, nil
}

// listedFiles is the sentence a validation failure ends with when files
// listed under "include" were read. The merged result is checked as a
// whole, because a listed file may lean on a provider another file defines,
// so a problem it finds is not in any one file. Without this the error
// named the file that lists the others, which is the one that is fine.
func listedFiles(read []string) string {
	if len(read) == 0 {
		return ""
	}
	return " (read as part of it, from \"include\": " + strings.Join(read, ", ") + ")"
}

// loadIncluded reads the files owner lists under "include", in order, and
// returns each as a Config.
//
// Not followed from inside: a listed file's own "include" is said and
// dropped. The list is one flat place to read what was read, and a file
// that could name more files would make that a tree, with a cycle to
// guard against and a person left to guess which file brought in which.
func loadIncluded(owner *Config, ownerPath, home string) ([]*Config, []string, []string, []string) {
	var layers []*Config
	var notes, setAside, read []string
	seen := map[string]bool{filepath.Clean(ownerPath): true}
	for _, entry := range owner.Include {
		path, ok, err := resolveInclude(entry, filepath.Dir(ownerPath), home)
		if err != nil {
			setAside = append(setAside, fmt.Sprintf("include %q in %s: %v", entry, ownerPath, err))
			continue
		}
		if !ok || seen[path] {
			// Empty once {env:} was replaced, or the same file twice, or
			// the file itself: nothing to read, and nothing worth a line.
			continue
		}
		seen[path] = true

		c, cNotes, err := loadOptional(path)
		switch {
		case err != nil:
			setAside = append(setAside, err.Error())
		case c == nil:
			setAside = append(setAside, fmt.Sprintf("include %s: no such file (listed in %s)", path, ownerPath))
		default:
			if len(c.Include) > 0 {
				notes = append(notes, fmt.Sprintf("include in %s is not followed; list those files in %s instead", path, ownerPath))
				c.Include = nil
			}
			notes = append(notes, cNotes...)
			layers = append(layers, c)
			read = append(read, path)
		}
	}
	return layers, notes, setAside, read
}

// resolveInclude turns one "include" entry into the path to read.
//
// "~/x" is under the home directory, an absolute path is itself, and
// anything else is relative to the directory of the file that lists it,
// which is the one place a relative path means the same thing wherever
// localcode was started from. ok is false for an entry that is empty, which
// is what "{env:OPENCODE_CONFIG:-}" becomes when the variable is not set:
// naming a file that may not be there is the point of that spelling, so it
// is skipped rather than reported.
func resolveInclude(entry, baseDir, home string) (path string, ok bool, err error) {
	e := strings.TrimSpace(entry)
	if e == "" {
		return "", false, nil
	}
	switch {
	case e == "~" || strings.HasPrefix(e, "~/") || strings.HasPrefix(e, `~\`):
		if home == "" {
			return "", false, fmt.Errorf("~ needs a home directory and none could be found")
		}
		// An entry that starts "~\" was written on Windows, and every
		// backslash in it is a separator wherever this runs, so a
		// config.json written there reads the same on a Mac. FromSlash
		// alone only does that on Windows. An entry that starts "~/" is
		// left as it is: on Unix a backslash in it is a letter in a file
		// name, and on Windows FromSlash and Join already read it as a
		// separator.
		rest := e[1:]
		if strings.HasPrefix(e, `~\`) {
			rest = strings.ReplaceAll(rest, `\`, "/")
		}
		e = filepath.Join(home, filepath.FromSlash(strings.TrimLeft(rest, "/")))
	case filepath.IsAbs(e):
	default:
		e = filepath.Join(baseDir, filepath.FromSlash(e))
	}
	return filepath.Clean(e), true, nil
}

// Load reads and validates a single config file from path, and the files
// it lists under "include". Its second return is LoadMerged's, for the
// same reason.
func Load(path string) (*Config, []string, error) {
	// Without a home directory only an entry that starts with "~" cannot
	// be read, and resolveInclude says so for that entry. Failing here
	// would refuse a file that needs none.
	home, _ := os.UserHomeDir()
	cfg, notes, setAside, read, err := loadOne(path, home)
	if err != nil {
		return nil, nil, err
	}
	if cfg == nil {
		return nil, nil, fmt.Errorf("config file not found: %s", path)
	}
	if err := cfg.Validate(); err != nil {
		return nil, nil, fmt.Errorf("invalid config %s: %w%s", path, err, listedFiles(read))
	}
	for _, s := range setAside {
		notes = append(notes, "set aside and not read: "+s)
	}
	return cfg, notes, nil
}

// LoadFile reads a single config file for editing (e.g. by `localcode
// mcp`). Unlike Load, a missing file is not an error — it returns an
// empty, unvalidated Config ready to be filled in and saved.
func LoadFile(path string) (*Config, error) {
	cfg, _, err := loadOptional(path)
	if err != nil {
		return nil, err
	}
	if cfg == nil {
		return &Config{}, nil
	}
	return cfg, nil
}

func loadOptional(path string) (*Config, []string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil, nil
		}
		return nil, nil, fmt.Errorf("read config %s: %w", path, err)
	}
	// Comments out first, so everything after this is ordinary JSON and
	// nothing downstream has to know the file could carry them. Blanked
	// rather than deleted, so a parse error's offset still points at the
	// line it came from. See jsonc.go.
	data = stripComments(data)
	// {env:NAME} next, so every field of every version of this struct
	// gets it without anything here having to know which fields are
	// secrets. See env.go.
	expanded, err := expandEnv(data, osLookup)
	if err != nil {
		return nil, nil, fmt.Errorf("config %s: %w", path, err)
	}
	// opencode's spellings after that, on values that are already what
	// they will be: this reads a model string, an npm name and a base
	// URL to decide what they mean, and a placeholder in any of them
	// would read as literal text.
	norm, err := NormalizeOpencode(expanded)
	if err != nil {
		return nil, nil, fmt.Errorf("config %s: %w", path, err)
	}
	var cfg Config
	if err := json.Unmarshal(norm.JSON, &cfg); err != nil {
		return nil, nil, fmt.Errorf("parse config %s: %w", path, err)
	}
	// And the one thing the substitution above could not do, because the
	// file did not ask for it in localcode's spelling: opencode names the
	// variable holding a provider's key rather than the key. Applied here,
	// to that field, rather than by writing a placeholder into the
	// document and expanding the whole of it a second time.
	for provider, envVar := range norm.EnvKeys {
		pc, ok := cfg.Providers[provider]
		if !ok || pc.APIKey != "" {
			continue
		}
		if v, found := osLookup(envVar); found {
			pc.APIKey = v
			cfg.Providers[provider] = pc
		}
	}
	return &cfg, norm.Ignored, nil
}

// mergeMap copies every entry of src into *dst, creating *dst if it was nil.
// Used by merge for each of Config's map-typed fields so they don't each
// need their own copy of the same three lines — and so a future map field
// can't quietly reuse copy-pasted merge logic that's subtly wrong.
func mergeMap[K comparable, V any](dst *map[K]V, src map[K]V) {
	if len(src) == 0 {
		return
	}
	if *dst == nil {
		*dst = map[K]V{}
	}
	for k, v := range src {
		(*dst)[k] = v
	}
}

// merge overlays other on top of c, with other's entries taking priority.
//
// Every field of Config must be handled here or it is silently dropped when
// both a global and a project config exist — see TestMergeFieldsGuard,
// which fails if a new Config field is added without a conscious decision
// about how (or whether) it merges.
func (c *Config) merge(other *Config) {
	if other == nil {
		return
	}
	mergeMap(&c.Providers, other.Providers)
	mergeMap(&c.Profiles, other.Profiles)
	mergeMap(&c.Agents, other.Agents)
	mergeMap(&c.MCPServers, other.MCPServers)
	mergeMap(&c.Permissions, other.Permissions)
	if other.DefaultProfile != "" {
		c.DefaultProfile = other.DefaultProfile
	}
	if other.Shell != "" {
		c.Shell = other.Shell
	}
	if other.DefaultAgent != "" {
		c.DefaultAgent = other.DefaultAgent
	}
	if other.MaxConcurrentTasks != 0 {
		c.MaxConcurrentTasks = other.MaxConcurrentTasks
	}
	if other.SubagentDepth != nil {
		c.SubagentDepth = other.SubagentDepth
	}
	if other.Instructions != nil {
		c.Instructions = other.Instructions
	}
	if other.AutoMemoryEnabled != nil {
		c.AutoMemoryEnabled = other.AutoMemoryEnabled
	}
	if other.AutoCompactEnabled != nil {
		c.AutoCompactEnabled = other.AutoCompactEnabled
	}
	// Merged, so a project can say "not while working here" — a checkout
	// pinned to a version, or one whose tooling is being bisected, has a
	// real reason not to be moved off the build it was left on.
	if other.AutoUpdate != nil {
		c.AutoUpdate = other.AutoUpdate
	}
	if other.AutoCompactPercent != 0 {
		c.AutoCompactPercent = other.AutoCompactPercent
	}
	if other.KeepGoingEnabled != nil {
		c.KeepGoingEnabled = other.KeepGoingEnabled
	}
	if other.RepeatLimitSteps != nil {
		c.RepeatLimitSteps = other.RepeatLimitSteps
	}
	if other.ShowTPS != nil {
		c.ShowTPS = other.ShowTPS
	}
	if other.ShowThinking != nil {
		c.ShowThinking = other.ShowThinking
	}
	if other.FoldThinking != nil {
		c.FoldThinking = other.FoldThinking
	}
	if other.ShowTimestamps != nil {
		c.ShowTimestamps = other.ShowTimestamps
	}
	// A project where the mouse stays with the terminal (shared
	// editing over a multiplexer, say) can say so, the same way it
	// can for the display switches beside this one.
	if other.Mouse != nil {
		c.Mouse = other.Mouse
	}
	if other.AutoDelegate != nil {
		c.AutoDelegate = other.AutoDelegate
	}
	// Wholesale rather than field by field, the way AutoDelegate is: an
	// allow list is a set, and merging two of them would produce a third
	// nobody wrote — the project quietly widening what the home
	// directory permits is the wrong direction for this particular
	// setting to be wrong in.
	if other.Network != nil {
		c.Network = other.Network
	}
	if other.SkipPermissions != nil {
		c.SkipPermissions = other.SkipPermissions
	}
	if other.UpdateURL != "" {
		c.UpdateURL = other.UpdateURL
	}
	// The project's own, and it has to be: how a project is checked is a
	// fact about the project, so a repository's config.json naming its
	// test command must win over whatever is in the home directory.
	if other.VerifyCommand != "" {
		c.VerifyCommand = other.VerifyCommand
	}
	if other.SkipToolPermissions != nil {
		c.SkipToolPermissions = other.SkipToolPermissions
	}
	if other.ReadOutsideWorkspace != nil {
		c.ReadOutsideWorkspace = other.ReadOutsideWorkspace
	}
	if other.WriteOutsideWorkspace != nil {
		c.WriteOutsideWorkspace = other.WriteOutsideWorkspace
	}
	if other.Orchestrate != nil {
		c.Orchestrate = other.Orchestrate
	}
	if other.ModelInvocable != nil {
		c.ModelInvocable = other.ModelInvocable
	}
	// Replaced rather than concatenated. A project naming which built-ins
	// the model may run is answering the question for that project, and
	// appending would let a global list add commands to a project that
	// listed a shorter one deliberately — which is the direction that
	// matters here.
	//
	// "Empty" is an answer, not the lack of one. A file that writes
	// "model_commands": [] has said the model may run no built-in command,
	// and a list from a lower layer must not survive that. Nil is the lack
	// of an answer, which is what an absent key leaves.
	if other.ModelCommands != nil {
		c.ModelCommands = other.ModelCommands
	}
	if other.SmartAgent != nil {
		c.SmartAgent = other.SmartAgent
	}
	if other.TraceMaxAgeDays != 0 {
		c.TraceMaxAgeDays = other.TraceMaxAgeDays
	}
	if other.TraceMaxTotalMB != 0 {
		c.TraceMaxTotalMB = other.TraceMaxTotalMB
	}
	for event, list := range other.Hooks {
		if c.Hooks == nil {
			c.Hooks = hooks.Config{}
		}
		c.Hooks[event] = list
	}
}
