package config

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestOpencodeMCPServersWorking verifies that a top-level mcp block is rewritten
// to mcp_servers and that the decoded server config resolves to a working transport.
func TestOpencodeMCPServersWorking(t *testing.T) {
	cfg, err := loadText(t, `{`+workingProviders+`,`+
		`"mcp":{"fs":{"type":"local","command":["npx","server"]}}}`)
	if err != nil {
		t.Fatalf("config with mcp block failed to load: %v", err)
	}
	server, ok := cfg.MCPServers["fs"]
	if !ok {
		t.Fatal("mcp entry was not moved to MCPServers")
	}
	if server.Transport() != MCPTransportStdio {
		t.Errorf("Transport() = %q, want %q", server.Transport(), MCPTransportStdio)
	}
	if server.Command != "npx" {
		t.Errorf("Command = %q, want %q", server.Command, "npx")
	}
	if len(server.Args) != 1 || server.Args[0] != "server" {
		t.Errorf("Args = %v, want [server]", server.Args)
	}
}

// TestOpencodeToolsBashFalseDenies verifies that {"tools":{"bash":false}} causes
// ResolvePermissionFor to return DecisionDeny.
func TestOpencodeToolsBashFalseDenies(t *testing.T) {
	cfg, err := loadText(t, `{`+workingProviders+`,"tools":{"bash":false}}`)
	if err != nil {
		t.Fatalf("config with tools block failed to load: %v", err)
	}
	got := cfg.ResolvePermissionFor(context.Background(), "bash", "ls", true)
	if got != DecisionDeny {
		t.Errorf("ResolvePermissionFor(bash) = %v, want %v (deny)", got, DecisionDeny)
	}
}

// TestOpencodeToolsBashTrueAllows verifies that {"tools":{"bash":true}} causes
// ResolvePermissionFor to return DecisionAllow, which opens calls that the static default
// would have paused to prompt for.
func TestOpencodeToolsBashTrueAllows(t *testing.T) {
	cfg, err := loadText(t, `{`+workingProviders+`,"tools":{"bash":true}}`)
	if err != nil {
		t.Fatalf("config with tools block failed to load: %v", err)
	}
	got := cfg.ResolvePermissionFor(context.Background(), "bash", "ls", true)
	if got != DecisionAllow {
		t.Errorf("ResolvePermissionFor(bash) = %v, want %v (allow)", got, DecisionAllow)
	}
}

// TestOpencodeToolsWriteMapsToEdit verifies that write and apply_patch map to edit
// and therefore govern both edit and write_file tools.
func TestOpencodeToolsWriteMapsToEdit(t *testing.T) {
	cfg, err := loadText(t, `{`+workingProviders+`,"tools":{"write":false}}`)
	if err != nil {
		t.Fatalf("config with tools.write failed to load: %v", err)
	}
	if got := cfg.ResolvePermissionFor(context.Background(), "edit", "file.go", true); got != DecisionDeny {
		t.Errorf("edit decision = %v, want %v", got, DecisionDeny)
	}
	if got := cfg.ResolvePermissionFor(context.Background(), "write_file", "file.go", true); got != DecisionDeny {
		t.Errorf("write_file decision = %v, want %v", got, DecisionDeny)
	}

	cfgPatch, err := loadText(t, `{`+workingProviders+`,"tools":{"apply_patch":false}}`)
	if err != nil {
		t.Fatalf("config with tools.apply_patch failed to load: %v", err)
	}
	if got := cfgPatch.ResolvePermissionFor(context.Background(), "edit", "file.go", true); got != DecisionDeny {
		t.Errorf("edit decision under apply_patch = %v, want %v", got, DecisionDeny)
	}
}

// Two spellings of the MCP block hold lists, not values, so they are
// joined — and only a server named in both is refused.
//
// This used to refuse the pair outright, which was fail-closed and
// therefore harmless, but wrong: "mcp" naming one server and
// "mcp_servers" naming another is two halves of one list, and a file
// that arrived that way (a localcode config with an opencode block
// pasted into it) had nothing contradictory in it.
func TestTheTwoMCPSpellingsAreJoinedUnlessTheyNameTheSameServer(t *testing.T) {
	cfg, err := loadText(t, `{`+workingProviders+`,`+
		`"mcp":{"s1":{"type":"stdio","command":"c1"}},`+
		`"mcp_servers":{"s2":{"type":"stdio","command":"c2"}}}`)
	if err != nil {
		t.Fatalf("two disjoint halves of one server list were refused: %v", err)
	}
	for _, name := range []string{"s1", "s2"} {
		if _, ok := cfg.MCPServers[name]; !ok {
			t.Errorf("server %q did not arrive; servers = %v", name, cfg.MCPServers)
		}
	}

	// The same name in both is the file saying two things about one
	// server, and picking a winner would leave the other read by nobody.
	_, err = loadText(t, `{`+workingProviders+`,`+
		`"mcp":{"s1":{"type":"stdio","command":"c1"}},`+
		`"mcp_servers":{"s1":{"type":"stdio","command":"c2"}}}`)
	if err == nil {
		t.Fatal("a server named in both spellings was accepted, so one of the two was read by nobody")
	}
	if !strings.Contains(err.Error(), `"s1"`) {
		t.Errorf("the refusal does not name the server: %v", err)
	}
}

// TestOpencodeToolAndPermissionCollisionRefused verifies that a tool configured in both
// tools and permission is refused, naming both spellings and the tool.
func TestOpencodeToolAndPermissionCollisionRefused(t *testing.T) {
	_, err := loadText(t, `{`+workingProviders+`,`+
		`"tools":{"bash":false},"permission":{"bash":"allow"}}`)
	if err == nil {
		t.Fatal("tool in both tools and permission loaded without error, want refusal")
	}
	errStr := err.Error()
	if !strings.Contains(errStr, "tools") || !strings.Contains(errStr, "permission") || !strings.Contains(errStr, "bash") {
		t.Errorf("error %q does not name tools, permission, and bash", errStr)
	}
}

// TestOpencodeCompactionAutoHonoured verifies that compaction.auto maps to
// auto_compact_enabled.
func TestOpencodeCompactionAutoHonoured(t *testing.T) {
	for _, tc := range []struct {
		val  bool
		want bool
	}{
		{false, false},
		{true, true},
	} {
		jsonStr := `{` + workingProviders + `,"compaction":{"auto":false}}`
		if tc.val {
			jsonStr = `{` + workingProviders + `,"compaction":{"auto":true}}`
		}
		cfg, err := loadText(t, jsonStr)
		if err != nil {
			t.Fatalf("compaction.auto:%v failed: %v", tc.val, err)
		}
		if cfg.AutoCompactEnabled == nil {
			t.Fatalf("compaction.auto:%v left AutoCompactEnabled nil", tc.val)
		}
		if *cfg.AutoCompactEnabled != tc.want {
			t.Errorf("compaction.auto:%v gave AutoCompactEnabled = %v, want %v", tc.val, *cfg.AutoCompactEnabled, tc.want)
		}
	}
}

// TestOpencodeCompactionAutoConflictRefused verifies that disagreeing auto_compact_enabled
// and compaction.auto are refused, naming both.
func TestOpencodeCompactionAutoConflictRefused(t *testing.T) {
	_, err := loadText(t, `{`+workingProviders+`,"auto_compact_enabled":true,"compaction":{"auto":false}}`)
	if err == nil {
		t.Fatal("disagreeing auto_compact_enabled and compaction.auto loaded without error, want refusal")
	}
	errStr := err.Error()
	if !strings.Contains(errStr, "auto_compact_enabled") || !strings.Contains(errStr, "compaction.auto") {
		t.Errorf("error %q does not name both auto_compact_enabled and compaction.auto", errStr)
	}
}

// TestOpencodeStep2RefusalsVerbatim verifies that each standalone refusal in Step 2
// fires with the exact sentence specified in the disposition table.
func TestOpencodeStep2RefusalsVerbatim(t *testing.T) {
	cases := []struct {
		name         string
		jsonSnippet  string
		wantSentence string
	}{
		{
			name:         "small_model",
			jsonSnippet:  `"small_model":"openai/gpt-4o"`,
			wantSentence: `small_model names a second model for lightweight work, and localcode has no title-generation or cheap-utility lane to point it at — its only cheap lane, the Smart Agent "quick" category, delegates real work rather than trivia. Remove it, or write a profile named "smart-quick" if that is what you meant.`,
		},
		{
			name:         "compaction.prune",
			jsonSnippet:  `"compaction":{"prune":true}`,
			wantSentence: `compaction.prune asks localcode to drop old tool outputs from the history it sends, and localcode has no pruning pass to turn on. Remove it, or use compaction.auto, which localcode does honour.`,
		},
		{
			name:         "compaction.tail_turns",
			jsonSnippet:  `"compaction":{"tail_turns":5}`,
			wantSentence: `compaction.tail_turns says how many recent turns to keep verbatim when compacting, and localcode's compaction has no turn-count retention to set. Remove it; compaction.auto is the part localcode can honour.`,
		},
		{
			name:         "compaction.preserve_recent_tokens",
			jsonSnippet:  `"compaction":{"preserve_recent_tokens":1000}`,
			wantSentence: `compaction.preserve_recent_tokens sets how much recent history survives compaction verbatim, and localcode's compaction has no such budget. Remove it; compaction.auto is the part localcode can honour.`,
		},
		{
			name:         "compaction.reserved",
			jsonSnippet:  `"compaction":{"reserved":2000}`,
			wantSentence: `compaction.reserved reserves a slice of the context window so compaction itself cannot overflow it, and localcode sizes that headroom itself with no setting to override. Remove it; compaction.auto is the part localcode can honour.`,
		},
		{
			name:         "tool_output",
			jsonSnippet:  `"tool_output":{"max_bytes":1000}`,
			wantSentence: `tool_output sets how much of a tool result reaches the model, and localcode sizes that from the model's context window instead, with no setting to override it and nowhere that keeps the part it cuts. Remove it; a large result is already trimmed, but to localcode's budget rather than yours.`,
		},
		{
			name:         "snapshot",
			jsonSnippet:  `"snapshot":false`,
			wantSentence: `snapshot: false asks localcode not to record file snapshots, and localcode copies every file a turn edits before changing it so /rewind can put it back. There is no setting to turn that off. Remove the key, or accept that the copies are made.`,
		},
		{
			name:         "plugin",
			jsonSnippet:  `"plugin":["plugin-a"]`,
			wantSentence: `plugin names code for localcode to load at startup, and localcode does not install or import anything at runtime. Remove the key; a plugin's tools will not be there and its checks will not run.`,
		},
		{
			name:         "enterprise",
			jsonSnippet:  `"enterprise":{"url":"https://corp.example.com"}`,
			wantSentence: `enterprise.url points localcode at an organisation server for configuration, and localcode reads its configuration only from the files on this machine. Remove the key; whatever that server mandates will not be applied here.`,
		},
		{
			name:         "experimental",
			jsonSnippet:  `"experimental":{"primary_tools":["bash"]}`,
			wantSentence: `experimental configures opencode internals that localcode does not have, and two of its fields — primary_tools and policies — take access away rather than add it, so ignoring them would leave localcode more permissive than your file. Remove the key.`,
		},
		{
			name:         "attachment",
			jsonSnippet:  `"attachment":{"image":{"max_width":1000}}`,
			wantSentence: `attachment limits the size of an image before it is sent to the model, and localcode sends images as they are, bounded only by a 32MB cap on the upload itself. Remove the key; an image larger than your limits will still be sent whole.`,
		},
		{
			name:         "command",
			jsonSnippet:  `"command":{"test":{"template":"echo test"}}`,
			wantSentence: `command defines slash commands inside the config file, and localcode reads commands only from files. Move each entry to .opencode/command/<name>.md — the template becomes the body, and description, agent and model become its frontmatter — and localcode will pick it up as it stands.`,
		},
		{
			name:         "skills.paths",
			jsonSnippet:  `"skills":{"paths":["/skills"]}`,
			wantSentence: `skills.paths adds skill folders from elsewhere on the disk, and localcode reads skills only from the project and the home directory it resolves. Remove it, or put the skills under .opencode/skills, which localcode already reads.`,
		},
		{
			name:         "skills.urls",
			jsonSnippet:  `"skills":{"urls":["https://example.com/skill"]}`,
			wantSentence: `skills.urls fetches skills over the network, and localcode does not fetch text the model follows from the network. Remove it, and keep the skills you want in .opencode/skills, which localcode already reads.`,
		},
		{
			name:         "references",
			jsonSnippet:  `"references":{"repo":"git@example.com:foo/bar"}`,
			wantSentence: `references makes directories outside this project, and cloned git repositories, readable as part of it, and localcode decides what may be read outside the workspace with read_outside_workspace and the external_directory permission instead. Remove the key; a directory listed here is not readable, and no repository is cloned.`,
		},
		{
			name:         "reference",
			jsonSnippet:  `"reference":{"repo":"git@example.com:foo/bar"}`,
			wantSentence: `reference is opencode's older spelling of references, and localcode refuses both: a directory outside this project is governed by read_outside_workspace and the external_directory permission, not by an alias in the config. Remove the key.`,
		},
		{
			name:         "share_auto",
			jsonSnippet:  `"share":"auto"`,
			wantSentence: `share: "auto" asks for a session to be publishable to a share URL, and localcode has no sharing — nothing is ever published, and nothing here will publish it for you. Remove the key, or set it to "disabled", which is what localcode does.`,
		},
		{
			name:         "autoshare_true",
			jsonSnippet:  `"autoshare":true`,
			wantSentence: `autoshare: true asks for every new session to be published to a share URL, and localcode has no sharing — nothing is ever published. Remove the key; do not rely on this file to have turned sharing on.`,
		},
		{
			name:         "formatter_true",
			jsonSnippet:  `"formatter":true`,
			wantSentence: `formatter asks for files to be formatted after they are edited, and localcode does not run formatters. Remove the key, or format from a hook or your own command; files localcode edits are left exactly as written.`,
		},
		{
			name:         "lsp_true",
			jsonSnippet:  `"lsp":true`,
			wantSentence: `lsp configures language servers for localcode to start, and localcode starts none. Remove the key; there will be no diagnostics and no lsp tool.`,
		},
		{
			name:         "server",
			jsonSnippet:  `"server":{"port":1234}`,
			wantSentence: `server is not supported — localcode takes its listen address from the --listen flag, not from the config file, so run localcode --listen 127.0.0.1:1234 rather than "server": {"port": 1234}. mdns, mdnsDomain and cors have no localcode equivalent.`,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := loadText(t, `{`+workingProviders+`,`+tc.jsonSnippet+`}`)
			if err == nil {
				t.Fatalf("%s loaded without error, want refusal", tc.name)
			}
			if !strings.Contains(err.Error(), tc.wantSentence) {
				t.Errorf("%s refusal mismatch:\ngot:  %s\nwant: %s", tc.name, err.Error(), tc.wantSentence)
			}
		})
	}
}

// TestOpencodeStep2AcceptedValues verifies that accepted values in Step 2 load cleanly.
func TestOpencodeStep2AcceptedValues(t *testing.T) {
	snippets := []string{
		`"share":"disabled"`,
		`"autoshare":false`,
		`"formatter":false`,
		`"lsp":false`,
	}
	for _, snip := range snippets {
		t.Run(snip, func(t *testing.T) {
			if _, err := loadText(t, `{`+workingProviders+`,`+snip+`}`); err != nil {
				t.Errorf("%s was refused: %v", snip, err)
			}
		})
	}
}

// TestOpencodeInertKeysAppearInIgnored verifies that INERT keys (logLevel, watcher,
// username, layout) load cleanly and appear in the Ignored list, sorted.
func TestOpencodeInertKeysAppearInIgnored(t *testing.T) {
	body := `{` + workingProviders + `,` +
		`"logLevel":"DEBUG",` +
		`"watcher":{"ignore":["*.log"]},` +
		`"username":"developer",` +
		`"layout":"stretch"}`

	p := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg, notes, err := Load(p)
	if err != nil {
		t.Fatalf("loading inert keys failed: %v", err)
	}
	if cfg == nil {
		t.Fatal("cfg is nil")
	}

	wantNotes := []string{"layout", "logLevel", "username", "watcher"}
	if len(notes) != len(wantNotes) {
		t.Fatalf("got notes %v, want %v", notes, wantNotes)
	}
	for i := range wantNotes {
		if notes[i] != wantNotes[i] {
			t.Errorf("note[%d] = %q, want %q", i, notes[i], wantNotes[i])
		}
	}
}

// TestOpencodeRefusalsCollectAndSort verifies that multiple refusals are collected together
// and sorted by dotted path.
func TestOpencodeRefusalsCollectAndSort(t *testing.T) {
	body := []byte(`{` + workingProviders + `,` +
		`"small_model":"openai/gpt-4o",` +
		`"snapshot":false,` +
		`"plugin":["plugin-x"]}`)

	norm, err := NormalizeOpencode(body)
	if err == nil {
		t.Fatal("multiple refusals loaded without error, want refusal")
	}
	if len(norm.JSON) != 0 {
		t.Errorf("norm.JSON should be empty on refusal, got %s", string(norm.JSON))
	}

	errStr := err.Error()
	lines := strings.Split(errStr, "\n")
	if len(lines) != 3 {
		t.Fatalf("expected 3 lines of refusals, got %d:\n%s", len(lines), errStr)
	}

	// Sorted by dotted path: plugin < small_model < snapshot
	if !strings.HasPrefix(lines[0], "plugin names code") {
		t.Errorf("line[0] should be plugin refusal, got: %s", lines[0])
	}
	if !strings.HasPrefix(lines[1], "small_model names a second model") {
		t.Errorf("line[1] should be small_model refusal, got: %s", lines[1])
	}
	if !strings.HasPrefix(lines[2], "snapshot: false asks localcode not to record") {
		t.Errorf("line[2] should be snapshot refusal, got: %s", lines[2])
	}
}

// TestLocalcodeConfigRegressionGuardByteForByte verifies that a full, valid localcode config.json
// containing profiles, providers with a bedrock profile field, agents, mcp_servers, and permission
// is untouched by normalisation: the JSON bytes returned are byte-for-byte identical, and all fields
// and permissions resolve exactly as before.
func TestLocalcodeConfigRegressionGuardByteForByte(t *testing.T) {
	raw := []byte(`{` +
		`"providers":{"bedrock":{"type":"bedrock","region":"us-west-2","profile":"my-aws-profile"}},` +
		`"profiles":{"main":{"provider":"bedrock","model":"us.anthropic.claude-sonnet-4-6"}},` +
		`"default_profile":"main",` +
		`"agents":{"general-purpose":{"profile":"main"}},` +
		`"mcp_servers":{"srv":{"type":"stdio","command":"echo","args":["hi"]}},` +
		`"permission":{"bash":"allow"}` +
		`}`)

	norm, err := NormalizeOpencode(raw)
	if err != nil {
		t.Fatalf("valid localcode config failed normalisation: %v", err)
	}
	if !bytes.Equal(norm.JSON, raw) {
		t.Errorf("regression guard failed: norm.JSON is not byte-for-byte identical to input:\ngot:  %s\nwant: %s",
			string(norm.JSON), string(raw))
	}

	cfg, err := loadText(t, string(raw))
	if err != nil {
		t.Fatalf("valid localcode config failed to load: %v", err)
	}
	if p, ok := cfg.Providers["bedrock"]; !ok || p.Profile != "my-aws-profile" {
		t.Errorf("Providers[bedrock].Profile corrupted: got %v", p)
	}
	if pr, ok := cfg.Profiles["main"]; !ok || pr.Provider != "bedrock" {
		t.Errorf("Profiles[main].Provider corrupted: got %v", pr)
	}
	if ag, ok := cfg.Agents["general-purpose"]; !ok || ag.Profile != "main" {
		t.Errorf("Agents[general-purpose].Profile corrupted: got %v", ag)
	}
	if srv, ok := cfg.MCPServers["srv"]; !ok || srv.Command != "echo" {
		t.Errorf("MCPServers[srv] corrupted: got %v", srv)
	}
	if decision := cfg.ResolvePermissionFor(context.Background(), "bash", "echo hello", true); decision != DecisionAllow {
		t.Errorf("ResolvePermissionFor(bash) = %v, want allow", decision)
	}
}

// A bare decision string is opencode's way of saying "this for every
// tool", and translating a tools block beside it used to throw it away.
//
// The direction is what makes it worth a test of its own. The blanket
// disappeared and every tool except the one named in tools came back
// allow, so a file whose author had asked to be consulted about
// everything was consulted about nothing — in the one key this whole
// translation exists to get right.
func TestABlanketPermissionIsNotDroppedWhenToolsIsTranslated(t *testing.T) {
	_, err := NormalizeOpencode([]byte(`{"permission":"ask","tools":{"bash":false}}`))
	if err == nil {
		t.Fatal(`"permission": "ask" beside a tools block was accepted, which loses the blanket`)
	}
	for _, want := range []string{`"ask"`, "tools"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal does not mention %s: %v", want, err)
		}
	}

	// The ordinary object form beside tools is not affected: those collide
	// by name, and a name that does not collide merges.
	if _, err := NormalizeOpencode([]byte(`{"permission":{"edit":"deny"},"tools":{"bash":false}}`)); err != nil {
		t.Errorf("an object permission beside tools was refused: %v", err)
	}
}

// A refusal quotes the value the file has, not one the writer imagined.
func TestTheShareRefusalNamesTheValueTheFileHas(t *testing.T) {
	_, err := NormalizeOpencode([]byte(`{"share":"manual"}`))
	if err == nil {
		t.Fatal(`share: "manual" was accepted`)
	}
	if !strings.Contains(err.Error(), `"manual"`) {
		t.Errorf("the refusal does not name the value in the file: %v", err)
	}
	if strings.Contains(err.Error(), `"auto"`) {
		t.Errorf("the refusal names a value the file does not contain: %v", err)
	}
	// "disabled" is what localcode does anyway, so it is not a refusal.
	if _, err := NormalizeOpencode([]byte(`{"share":"disabled"}`)); err != nil {
		t.Errorf(`share: "disabled" was refused: %v`, err)
	}
}
