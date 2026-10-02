package agent

import (
	"encoding/json"
	"fmt"
	"strings"
)

// stageItems is what a fanout spreads over: the literal list, or the values
// of an earlier stage's kept results.
func (l *Loop) stageItems(stage Stage, results map[string][]outcome) ([]string, int) {
	if stage.Kind != "fanout" {
		return []string{""}, 0
	}
	ref, isRef := planRef(stage.Over)
	if !isRef {
		return stage.Over, 0
	}
	from, field, ok := splitRef(ref)
	if !ok {
		return nil, 0
	}
	// Merged, not concatenated. A fanout that reviews one change along
	// four dimensions and then spreads over what it found gets the same
	// finding back from every dimension that noticed it, so the skeptic
	// stage was launching four agents on one item and calling it four
	// findings. Measured on a two-dimension plan: two distinct findings
	// became four items and eight launches, half the run spent twice.
	//
	// First-seen order, and the count of what was merged goes into the
	// report: a silent merge is the same defect as a silent cap.
	seen := map[string]bool{}
	var items []string
	merged := 0
	for _, o := range results[from] {
		for _, v := range asStrings(o.data[field]) {
			key := strings.ToLower(strings.Join(strings.Fields(v), " "))
			if seen[key] {
				merged++
				continue
			}
			seen[key] = true
			items = append(items, v)
			if len(items) >= maxFanout {
				return items, merged
			}
		}
	}
	return items, merged
}

// carriedInput is what {{input}} expands to: every kept result so far,
// labelled by the stage that produced it.
//
// Composed by localcode rather than by a model, and capped, because it is
// the one part of a stage's prompt whose size nobody chose. A rerun of a
// repeat_until stage always carries the rounds so far, even when the
// prompt never asked for {{input}}: without that the next round works
// blind, repeating whatever the last one tried.
func (l *Loop) carriedInput(p Plan, stage Stage, results map[string][]outcome) string {
	repeat := stage.RepeatUntil != "" && len(results[stage.Name]) > 0
	if !strings.Contains(stage.Prompt, "{{input}}") && stage.Kind != "barrier" && !repeat {
		return ""
	}
	var b strings.Builder
	for _, s := range p.Stages {
		if s.Name == stage.Name {
			if repeat {
				writeKept(&b, s, results[s.Name], "earlier rounds")
			}
			break
		}
		kept := results[s.Name]
		if len(kept) == 0 {
			continue
		}
		writeKept(&b, s, kept, "")
	}
	return truncateMiddle(b.String(), carriedInputLimit,
		"earlier stages produced more than fits in one prompt")
}

// writeKept appends one stage's kept results under its heading. Tag names
// the section when it is not the ordinary carried input: the earlier rounds
// of the stage being rerun.
func writeKept(b *strings.Builder, s Stage, kept []outcome, tag string) {
	heading := fmt.Sprintf("## %s (%s, %d kept)", s.Name, s.Agent, len(kept))
	if tag != "" {
		heading += " " + tag
	}
	b.WriteString(heading + "\n")
	for _, o := range kept {
		if o.data != nil {
			enc, _ := json.Marshal(o.data)
			b.Write(enc)
			b.WriteByte('\n')
			continue
		}
		b.WriteString(strings.TrimSpace(o.text))
		b.WriteString("\n")
	}
	b.WriteString("\n")
}

// carriedInputLimit bounds what one stage is handed from the stages before
// it. Thirty-two thousand characters is roughly eight thousand tokens: a
// large brief, and a long way short of a window.
const carriedInputLimit = 32000

func truthy(v any) bool {
	switch t := v.(type) {
	case nil:
		return false
	case bool:
		return t
	case string:
		return strings.TrimSpace(t) != ""
	case float64:
		return t != 0
	case []any:
		return len(t) > 0
	default:
		return true
	}
}

func asStrings(v any) []string {
	switch t := v.(type) {
	case string:
		if strings.TrimSpace(t) == "" {
			return nil
		}
		return []string{t}
	case []any:
		var out []string
		for _, e := range t {
			if s, ok := e.(string); ok && strings.TrimSpace(s) != "" {
				out = append(out, s)
			}
		}
		return out
	default:
		return nil
	}
}
