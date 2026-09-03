package agent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"localcode/internal/events"
	"localcode/internal/provider"
)

// "/llm-doctor" is for the day a local model that answered well yesterday
// loops today.
//
// localcode is a client. It cannot read the server's startup flags or its
// logs, which is where the cause of such a day lives. What a client can
// do is ask the server what it says about itself, send it the same few
// requests every time, judge the answers, and keep the result — so a
// later run can say what differs from a run taken when things were good.
// That is the evidence this produces: "the server reports different facts
// than at the baseline" and "the same request bytes now get a different
// answer", with localcode's own version beside them so a change on the
// client side is named rather than blamed on the server. It is not a
// cause, and the report does not claim one.
//
// Only for models whose name says muse or gemma: the families this
// project runs locally, on servers that are the person's own to fix. On
// anything else the command says so and sends nothing.
//
// The first run is not made the baseline on its own. A run taken while
// the model is misbehaving would make tomorrow's healthy one look like
// the change; "/llm-doctor baseline" is the person saying "this is what
// good looks like".

const (
	doctorTimeout       = 5 * time.Minute
	doctorFactsTimeout  = 10 * time.Second
	doctorCanaryTimeout = 90 * time.Second
	// A fixed seed, so a server that honours one answers the same way
	// twice. Servers that do not know the field ignore it.
	doctorSeed = 7
)

// doctorApplies is the gate: the feature exists for muse and gemma only.
func doctorApplies(model string) bool {
	m := strings.ToLower(model)
	return strings.Contains(m, "muse") || strings.Contains(m, "gemma")
}

// doctorRun is one execution of the command, and what is kept.
type doctorRun struct {
	At        time.Time            `json:"at"`
	Localcode string               `json:"localcode"`
	Model     string               `json:"model"`
	Provider  string               `json:"provider"`
	BaseURL   string               `json:"base_url"`
	Server    provider.ServerFacts `json:"server"`
	Canaries  []doctorResult       `json:"canaries"`
}

type doctorResult struct {
	Name         string          `json:"name"`
	Pass         bool            `json:"pass"`
	Why          string          `json:"why"`
	FinishReason string          `json:"finish_reason,omitempty"`
	OutputTokens int             `json:"output_tokens,omitempty"`
	Millis       int64           `json:"millis"`
	Reply        string          `json:"reply,omitempty"`
	RequestSHA   string          `json:"request_sha256"`
	Request      json.RawMessage `json:"request"`
}

// doctorFile is what sits under ~/.localcode/doctor, one per model.
type doctorFile struct {
	Baseline *doctorRun `json:"baseline,omitempty"`
	Last     *doctorRun `json:"last,omitempty"`
}

// A canary is a request whose right answer is known before it is sent.
// Four of them, each after one thing a coding agent cannot do without:
// a tool call that comes back structured, a code change, an instruction
// followed to the letter, and an answer that ends.
type doctorCanary struct {
	name  string
	build func(model string) map[string]any
	judge func(r provider.RawReply) (bool, string)
}

func doctorRequest(model, user string, maxTokens int, tools []map[string]any) map[string]any {
	body := map[string]any{
		"model":       model,
		"messages":    []map[string]any{{"role": "user", "content": user}},
		"max_tokens":  maxTokens,
		"temperature": 0,
		"seed":        doctorSeed,
		"stream":      false,
	}
	if tools != nil {
		body["tools"] = tools
		body["tool_choice"] = "auto"
	}
	return body
}

var doctorReadFileTool = []map[string]any{{
	"type": "function",
	"function": map[string]any{
		"name":        "read_file",
		"description": "Read a file from the workspace.",
		"parameters": map[string]any{
			"type": "object",
			"properties": map[string]any{
				"path": map[string]any{"type": "string", "description": "Path of the file to read."},
			},
			"required": []string{"path"},
		},
	},
}}

var doctorCanaries = []doctorCanary{
	{
		name: "tool_call",
		build: func(model string) map[string]any {
			return doctorRequest(model, "Use the read_file tool to read main.go. Call the tool; do not describe it.", 128, doctorReadFileTool)
		},
		judge: func(r provider.RawReply) (bool, string) {
			if len(r.ToolCalls) > 0 && r.ToolCalls[0].Name == "read_file" {
				return true, "called read_file"
			}
			if len(r.ToolCalls) > 0 {
				return false, fmt.Sprintf("called %q rather than read_file", r.ToolCalls[0].Name)
			}
			if strings.Contains(r.Content, "read_file") {
				return false, "the call came back as text, not as a tool call: the server's tool-call parser is off or changed"
			}
			return false, "no tool call; replied " + doctorQuote(r.Content)
		},
	},
	{
		name: "code_fix",
		build: func(model string) map[string]any {
			return doctorRequest(model, "This Go function must return the sum of a and b. Reply with only the corrected function, no explanation.\n\nfunc add(a, b int) int {\n\treturn a - b\n}", 128, nil)
		},
		judge: func(r provider.RawReply) (bool, string) {
			flat := strings.Join(strings.Fields(r.Content), "")
			if strings.Contains(flat, "returna+b") || strings.Contains(flat, "returnb+a") {
				return true, "corrected"
			}
			return false, "the function was not corrected; replied " + doctorQuote(r.Content)
		},
	},
	{
		name: "exact_reply",
		build: func(model string) map[string]any {
			return doctorRequest(model, "Reply with exactly the word OK and nothing else.", 16, nil)
		},
		judge: func(r provider.RawReply) (bool, string) {
			t := strings.Trim(strings.TrimSpace(r.Content), "\"'`*.! ")
			if strings.EqualFold(t, "OK") {
				return true, "replied OK"
			}
			if strings.TrimSpace(r.Content) == "" && r.Reasoning != "" {
				return false, "content was empty; the answer went into reasoning_content"
			}
			return false, "replied " + doctorQuote(r.Content) + " rather than OK"
		},
	},
	{
		name: "stops",
		build: func(model string) map[string]any {
			return doctorRequest(model, "Write the numbers 1 to 5, one per line, then stop.", 64, nil)
		},
		judge: func(r provider.RawReply) (bool, string) {
			if r.FinishReason == "length" {
				return false, "did not stop within 64 tokens (finish_reason=length)"
			}
			if strings.Contains(r.Content, "5") {
				return true, "stopped on its own"
			}
			return false, "stopped before reaching 5; replied " + doctorQuote(r.Content)
		},
	},
}

// doctorQuote is a reply as it fits in one line of a report.
func doctorQuote(s string) string {
	return fmt.Sprintf("%q", doctorClip(strings.Join(strings.Fields(s), " "), 80))
}

func doctorClip(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}

// routeLLMDoctor answers "/llm-doctor" and "/llm-doctor baseline".
func (l *Loop) routeLLMDoctor(ctx context.Context, sessionID, agentName, text string) (bool, error) {
	fields := strings.Fields(text)
	if len(fields) == 0 || !strings.EqualFold(fields[0], "/llm-doctor") {
		return false, nil
	}
	l.Store.Append(sessionID, events.TypeUserMessage, map[string]any{"text": text, "local": true})

	arg := ""
	if len(fields) > 1 {
		arg = strings.ToLower(fields[1])
	}
	if len(fields) > 2 || (arg != "" && arg != "baseline") {
		return true, l.replyText(sessionID, "usage: /llm-doctor [baseline]")
	}

	// profileFor's first result is the profile's name; the provider is
	// the profile's own field, and it is the provider that is probed.
	_, profile, err := l.profileFor(ctx, agentName)
	if err != nil {
		return true, l.replyText(sessionID, "llm-doctor: "+err.Error())
	}
	key := profile.Provider
	if !doctorApplies(profile.Model) {
		return true, l.replyText(sessionID, fmt.Sprintf(
			"/llm-doctor is for muse and gemma models; this conversation's model is %s.", profile.Model))
	}
	oa, ok := l.Providers[key].(*provider.OpenAICompat)
	if !ok {
		kind := "another"
		if l.Config != nil {
			if pc, ok := l.Config.Providers[key]; ok && pc.Type != "" {
				kind = "a " + string(pc.Type)
			}
		}
		return true, l.replyText(sessionID, fmt.Sprintf(
			"/llm-doctor speaks to OpenAI-compatible servers such as vLLM; profile %q reaches %s through %s provider.", key, profile.Model, kind))
	}

	path := doctorPath(l.doctorDir(), profile.Model)
	file, _ := loadDoctorFile(path)

	if arg == "baseline" {
		if file.Last == nil {
			return true, l.replyText(sessionID,
				"no run to keep yet: run /llm-doctor first, on a day the model is behaving well, then /llm-doctor baseline.")
		}
		file.Baseline = file.Last
		if err := saveDoctorFile(path, file); err != nil {
			return true, l.replyText(sessionID, "llm-doctor: "+err.Error())
		}
		return true, l.replyText(sessionID, fmt.Sprintf(
			"kept the run of %s as the baseline for %s. Later runs will say what differs from it.",
			file.Baseline.At.Format("2006-01-02 15:04"), profile.Model))
	}

	runCtx, cancel := context.WithTimeout(ctx, doctorTimeout)
	defer cancel()
	run := l.runDoctor(runCtx, oa, key, profile.Model)
	file.Last = &run
	saveErr := saveDoctorFile(path, file)
	replays := writeDoctorReplays(path, run)
	report := doctorReport(run, file.Baseline, path, replays)
	if saveErr != nil {
		report += "\n\n(this run was not saved: " + saveErr.Error() + ")"
	}
	return true, l.replyText(sessionID, report)
}

func (l *Loop) runDoctor(ctx context.Context, p *provider.OpenAICompat, providerKey, model string) doctorRun {
	run := doctorRun{
		At:        time.Now(),
		Localcode: l.localcodeVersion(),
		Model:     model,
		Provider:  providerKey,
		BaseURL:   p.BaseURL,
	}
	factsCtx, cancel := context.WithTimeout(ctx, doctorFactsTimeout)
	run.Server = p.ServerFacts(factsCtx, model)
	cancel()

	for _, c := range doctorCanaries {
		// Maps marshal with sorted keys, so the bytes are the same every
		// run and the hash means "the same request".
		body, err := json.Marshal(c.build(model))
		if err != nil {
			run.Canaries = append(run.Canaries, doctorResult{Name: c.name, Why: "could not build the request: " + err.Error()})
			continue
		}
		sum := sha256.Sum256(body)
		res := doctorResult{Name: c.name, Request: body, RequestSHA: hex.EncodeToString(sum[:])}

		cctx, cancel := context.WithTimeout(ctx, doctorCanaryTimeout)
		start := time.Now()
		reply, err := p.RawChat(cctx, body)
		cancel()
		res.Millis = time.Since(start).Milliseconds()
		if err != nil {
			res.Why = "request failed: " + err.Error()
		} else {
			res.FinishReason = reply.FinishReason
			res.OutputTokens = reply.OutputTokens
			res.Reply = doctorClip(reply.Content, 200)
			res.Pass, res.Why = c.judge(reply)
		}
		run.Canaries = append(run.Canaries, res)
	}
	return run
}

func (l *Loop) localcodeVersion() string {
	if l.Version == "" {
		return "dev"
	}
	return l.Version
}

// --- storage ---

func (l *Loop) doctorDir() string {
	if l.DoctorDir != "" {
		return l.DoctorDir
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return filepath.Join(os.TempDir(), "localcode-doctor")
	}
	return filepath.Join(home, ".localcode", "doctor")
}

var doctorSlugUnsafe = regexp.MustCompile(`[^a-z0-9._-]+`)

// doctorSlug is a model name as a file name: "google/gemma-3-27b-it"
// becomes "google-gemma-3-27b-it".
func doctorSlug(model string) string {
	s := doctorSlugUnsafe.ReplaceAllString(strings.ToLower(model), "-")
	s = strings.Trim(s, "-.")
	if len(s) > 80 {
		s = s[:80]
	}
	if s == "" {
		return "model"
	}
	return s
}

func doctorPath(dir, model string) string {
	return filepath.Join(dir, doctorSlug(model)+".json")
}

func loadDoctorFile(path string) (doctorFile, error) {
	var f doctorFile
	data, err := os.ReadFile(path)
	if err != nil {
		return f, err
	}
	return f, json.Unmarshal(data, &f)
}

func saveDoctorFile(path string, f doctorFile) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// writeDoctorReplays puts each failing canary's request beside the run
// file, so the server's operator can send the same bytes with curl and
// see the same answer. What was passing is not written: a replay is for
// the finding.
func writeDoctorReplays(path string, run doctorRun) []string {
	base := strings.TrimSuffix(path, ".json")
	var out []string
	for _, c := range run.Canaries {
		if c.Pass || len(c.Request) == 0 {
			continue
		}
		p := base + "." + c.Name + ".json"
		if err := os.WriteFile(p, c.Request, 0o644); err == nil {
			out = append(out, p)
		}
	}
	return out
}

// --- the report ---

func doctorReport(run doctorRun, base *doctorRun, path string, replays []string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "llm-doctor: %s at %s (localcode %s), %s\n",
		run.Model, doctorHost(run.BaseURL), run.Localcode, run.At.Format("2006-01-02 15:04"))

	b.WriteString("\nServer\n")
	s := run.Server
	if !s.ModelsOK {
		b.WriteString("- /v1/models: not answered\n")
	} else {
		fmt.Fprintf(&b, "- model id: %s\n", orNone(s.ModelID, "not in the list"))
		if s.MaxModelLen > 0 {
			fmt.Fprintf(&b, "- max_model_len: %d\n", s.MaxModelLen)
		} else {
			b.WriteString("- max_model_len: not reported\n")
		}
	}
	if s.VersionOK {
		fmt.Fprintf(&b, "- vLLM: %s\n", s.Version)
	} else {
		b.WriteString("- /version: not offered (not vLLM, or not exposed)\n")
	}
	if s.MetricsOK {
		if s.CacheDtype != "" {
			fmt.Fprintf(&b, "- kv cache dtype: %s\n", s.CacheDtype)
		}
		fmt.Fprintf(&b, "- preemptions %s · kv cache in use %s · running %s · waiting %s\n",
			doctorMetric(s.Metrics, "preemptions"), doctorPercent(s.Metrics, "kv_cache_usage"),
			doctorMetric(s.Metrics, "requests_running"), doctorMetric(s.Metrics, "requests_waiting"))
		fmt.Fprintf(&b, "- requests finished by stop %s · by length %s\n",
			doctorMetric(s.Metrics, "finished_stop"), doctorMetric(s.Metrics, "finished_length"))
	} else {
		b.WriteString("- /metrics: not offered\n")
	}

	passes := 0
	fmt.Fprintf(&b, "\nCanaries (temperature 0, seed %d)\n", doctorSeed)
	for _, c := range run.Canaries {
		verdict := "FAIL"
		if c.Pass {
			verdict = "pass"
			passes++
		}
		fmt.Fprintf(&b, "- %s: %s, %s (%s, %d tokens, %.1fs)\n",
			c.Name, verdict, c.Why, orNone(c.FinishReason, "no finish_reason"), c.OutputTokens, float64(c.Millis)/1000)
	}
	fmt.Fprintf(&b, "%d of %d pass\n", passes, len(run.Canaries))

	if base != nil {
		fmt.Fprintf(&b, "\nAgainst the baseline of %s (localcode %s)\n", base.At.Format("2006-01-02 15:04"), base.Localcode)
		diffs := doctorDiff(run, *base)
		if len(diffs) == 0 {
			b.WriteString("- nothing differs\n")
		}
		for _, d := range diffs {
			b.WriteString("- " + d + "\n")
		}
	}

	b.WriteString("\nWhat this shows\n")
	b.WriteString(doctorVerdict(run, base))

	fmt.Fprintf(&b, "\nSaved: %s", path)
	if len(replays) > 0 {
		fmt.Fprintf(&b, "\nReplay a failing canary yourself, or hand it to whoever runs the server:\n  curl -s %s/chat/completions -H 'Content-Type: application/json' -d @%s",
			run.BaseURL, replays[0])
	}
	return b.String()
}

// doctorDiff lists what a run changed against the baseline, in what a
// client can see: the server's facts, each canary's verdict, and
// localcode's own version.
func doctorDiff(run, base doctorRun) []string {
	var d []string
	if run.Server.ModelID != base.Server.ModelID {
		d = append(d, fmt.Sprintf("model id %s → %s", orNone(base.Server.ModelID, "none"), orNone(run.Server.ModelID, "none")))
	}
	if run.Server.MaxModelLen != base.Server.MaxModelLen {
		d = append(d, fmt.Sprintf("max_model_len %d → %d", base.Server.MaxModelLen, run.Server.MaxModelLen))
	}
	if run.Server.Version != base.Server.Version {
		d = append(d, fmt.Sprintf("vLLM %s → %s", orNone(base.Server.Version, "none"), orNone(run.Server.Version, "none")))
	}
	if run.Server.CacheDtype != base.Server.CacheDtype {
		d = append(d, fmt.Sprintf("kv cache dtype %s → %s", orNone(base.Server.CacheDtype, "none"), orNone(run.Server.CacheDtype, "none")))
	}
	baseBy := map[string]doctorResult{}
	for _, c := range base.Canaries {
		baseBy[c.Name] = c
	}
	for _, c := range run.Canaries {
		bc, ok := baseBy[c.Name]
		if !ok {
			continue
		}
		if bc.RequestSHA != c.RequestSHA {
			d = append(d, fmt.Sprintf("%s: the request itself changed (this localcode's canary is not the baseline's), so its verdict is not comparable", c.Name))
			continue
		}
		if bc.Pass != c.Pass {
			d = append(d, fmt.Sprintf("%s: %s → %s", c.Name, doctorPassWord(bc.Pass), doctorPassWord(c.Pass)))
		}
	}
	if run.Localcode != base.Localcode {
		d = append(d, fmt.Sprintf("localcode %s → %s", base.Localcode, run.Localcode))
	}
	return d
}

func doctorVerdict(run doctorRun, base *doctorRun) string {
	var failing []string
	for _, c := range run.Canaries {
		if !c.Pass {
			failing = append(failing, c.Name)
		}
	}
	if base == nil {
		if len(failing) == 0 {
			return "- Every canary passes. No baseline yet: /llm-doctor baseline keeps this run as the reference for a day the model answers differently.\n"
		}
		return fmt.Sprintf("- %d of %d canaries fail (%s). Without a baseline this cannot say whether that is new. On a day the model behaves well, run /llm-doctor and then /llm-doctor baseline; after that, a run like this one can say what changed.\n",
			len(failing), len(run.Canaries), strings.Join(failing, ", "))
	}

	baseBy := map[string]bool{}
	for _, c := range base.Canaries {
		baseBy[c.Name] = c.Pass
	}
	var newlyFailing, recovered []string
	for _, c := range run.Canaries {
		was, ok := baseBy[c.Name]
		if !ok {
			continue
		}
		switch {
		case was && !c.Pass:
			newlyFailing = append(newlyFailing, c.Name)
		case !was && c.Pass:
			recovered = append(recovered, c.Name)
		}
	}
	serverChanged := run.Server.ModelID != base.Server.ModelID ||
		run.Server.MaxModelLen != base.Server.MaxModelLen ||
		run.Server.Version != base.Server.Version ||
		run.Server.CacheDtype != base.Server.CacheDtype

	var b strings.Builder
	switch {
	case len(newlyFailing) > 0 && serverChanged:
		fmt.Fprintf(&b, "- The server reports different facts than at the baseline, and the same request bytes now get a different answer (%s). That points at the server. What changed there is in its startup flags and logs, which a client cannot see.\n",
			strings.Join(newlyFailing, ", "))
	case len(newlyFailing) > 0:
		fmt.Fprintf(&b, "- The server reports the same facts as at the baseline, yet the same request bytes now get a different answer (%s). Something a client cannot see changed on the server: the weights, the sampling defaults, the chat template, or the tool parser.\n",
			strings.Join(newlyFailing, ", "))
	case serverChanged:
		b.WriteString("- The server reports different facts than at the baseline, but every canary still answers as it did. If the model feels worse, the change is in what these canaries do not exercise: long contexts, or the shape of a real conversation.\n")
	default:
		b.WriteString("- Nothing differs from the baseline in what a client can see. If the model feels worse, it is not in the server's reported facts or in these canaries: look at the conversation itself (/context) and at the server's logs.\n")
	}
	if run.Localcode != base.Localcode {
		fmt.Fprintf(&b, "- localcode itself changed since the baseline (%s → %s). The canary bytes did not, so their verdicts still compare; how localcode builds a real request may not.\n",
			base.Localcode, run.Localcode)
	}
	if len(recovered) > 0 {
		fmt.Fprintf(&b, "- Recovered since the baseline: %s.\n", strings.Join(recovered, ", "))
	}
	return b.String()
}

func doctorPassWord(pass bool) string {
	if pass {
		return "pass"
	}
	return "FAIL"
}

func doctorHost(baseURL string) string {
	if u, err := url.Parse(baseURL); err == nil && u.Host != "" {
		return u.Host
	}
	return baseURL
}

func doctorMetric(m map[string]float64, key string) string {
	v, ok := m[key]
	if !ok {
		return "?"
	}
	if v == float64(int64(v)) {
		return fmt.Sprintf("%d", int64(v))
	}
	return fmt.Sprintf("%.1f", v)
}

// doctorPercent renders a gauge vLLM reports as a fraction of one.
func doctorPercent(m map[string]float64, key string) string {
	v, ok := m[key]
	if !ok {
		return "?"
	}
	return fmt.Sprintf("%.0f%%", v*100)
}

func orNone(s, none string) string {
	if s == "" {
		return none
	}
	return s
}
