package config

import "path/filepath"

// configSources is every file a merged configuration is built from by
// default, in the order each is laid over the one before it — so the last
// that exists wins a key the other also sets.
//
// Two files, both localcode's own: ~/.localcode/config.json and the
// project's .localcode/config.json. Two inputs and no lookups, because the
// order is a decision and the places are facts about this program rather
// than about this machine. A caller hands over the home directory and the
// project directory; whether either file is there is the loader's
// question, asked afterwards.
//
// Nothing written for another program is read from here. opencode keeps
// its config at ~/.config/opencode/opencode.json and in the project, and
// until v0.154.0 localcode went looking for both on every start. A person
// who never asked for that found another program's config deciding what
// their localcode did, and had no setting that turned it off. Now a file
// like that is read only when a localcode config names it under "include",
// which is also the one place to look to see what was read. Skills are
// the exception and are not a config file: see internal/userdirs.
//
// Where a project file sits relative to the global one is unchanged:
// global first, project over it, so a repository can say what is different
// about working in it.
func configSources(home, projectDir string) []source {
	return []source{
		{path: filepath.Join(home, ".localcode", "config.json")},
		{path: filepath.Join(projectDir, ".localcode", "config.json")},
	}
}

// source is one file the configuration is built from.
type source struct {
	path string
}
