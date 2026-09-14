package tui

import (
	"regexp"
	"strconv"
	"strings"
	"unicode"

	"charm.land/lipgloss/v2"
)

// Terminal rendering of model output, dependency-free.
//
// The Web UI renders the same text through internal/daemon/static/js/
// markdown.js, which Go cannot call, and glamour (the dependency-shaped
// answer) would pull goldmark and chroma into a build that has kept its
// module list deliberately short. So this is a second implementation kept
// to the same construct set — headings, emphasis, code, lists,
// blockquotes, links, rules — and no more. Where the two disagree on
// purpose (nested lists, which the browser flattens; tables, which pass
// through here as literal lines) the divergence is named at the site.
//
// The one hard rule: this runs at display time inside renderTranscript,
// never at append time. transcriptEntry.text stays raw markdown — the same
// bytes a log file receives — and the ANSI below exists only in the
// viewport's content. Styling the stored text would leak escape sequences
// into anything the transcript is written to.
var (
	mdHeadingStyle = lipgloss.NewStyle().Bold(true)
	mdBoldStyle    = lipgloss.NewStyle().Bold(true)
	mdItalicStyle  = lipgloss.NewStyle().Italic(true)
	mdCodeStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("2"))
	mdLinkStyle    = lipgloss.NewStyle().Underline(true).Foreground(lipgloss.Color("4"))
	mdQuoteStyle   = lipgloss.NewStyle().Faint(true)
	mdRuleStyle    = lipgloss.NewStyle().Faint(true)

	mdKeywordStyle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("5"))
	mdStringStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("2"))
	mdNumberStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("6"))
	mdCommentStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("8"))
)

// minModelWidth is the narrowest renderModelMarkdown lays out at. Below
// it nothing wraps honestly anyway, and a hostile width must degrade to
// a cramped render, never to a panic in the repeat or the wrapper.
const minModelWidth = 20

// renderModelMarkdown renders markdown source for the terminal at width
// columns. Prose is word-wrapped to width; code lines are not reflowed by
// the renderer itself (the viewport wraps them visually), so code is
// never reflowed into different bytes. Text with no markdown in it comes
// back unchanged apart from wrapping.
func renderModelMarkdown(src string, width int) string {
	if width < minModelWidth {
		width = minModelWidth
	}
	segs := splitMarkdownSegments(src)
	out := make([]string, 0, len(segs))
	for _, s := range segs {
		if s.code {
			out = append(out, renderCodeBlock(s.lang, s.lines))
			continue
		}
		out = append(out, renderTextLines(s.lines, width)...)
	}
	return strings.Join(out, "\n")
}

// mdSegment is one run of the source: either the lines inside a fenced
// code block (kept raw, never marked up) or the text between fences.
type mdSegment struct {
	code  bool
	lang  string
	lines []string
}

// splitMarkdownSegments pulls fenced code blocks out first, so nothing
// else touches their contents — a ``` line inside a fence would
// otherwise read as emphasis or a rule, and markup inside code is never
// markup. An unterminated trailing fence (a reply still streaming) is a
// block to the end of input rather than literal backticks, mirroring the
// browser renderer.
func splitMarkdownSegments(src string) []mdSegment {
	lines := strings.Split(src, "\n")
	segs := []mdSegment{}
	cur := mdSegment{}
	inFence := false
	flush := func() {
		if len(cur.lines) > 0 || cur.code {
			segs = append(segs, cur)
		}
		cur = mdSegment{}
	}
	for _, ln := range lines {
		if !inFence && strings.HasPrefix(ln, "```") {
			flush()
			inFence = true
			cur = mdSegment{code: true, lang: strings.TrimSpace(strings.TrimPrefix(ln, "```"))}
			continue
		}
		if inFence && strings.HasPrefix(ln, "```") {
			flush()
			inFence = false
			continue
		}
		cur.lines = append(cur.lines, ln)
	}
	flush()
	return segs
}

// renderCodeBlock draws a fenced block: each line highlighted for lang,
// indented two spaces so it reads as set apart from prose. One trailing
// empty line (the newline before the closing fence) is dropped, matching
// the browser; inner blank lines are kept.
func renderCodeBlock(lang string, lines []string) string {
	for len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	highlighted := highlightCode(strings.Join(lines, "\n"), lang)
	out := strings.Split(highlighted, "\n")
	for i, ln := range out {
		if strings.TrimSpace(stripANSI(ln)) != "" {
			out[i] = "  " + ln
		}
	}
	if len(out) == 0 {
		return ""
	}
	return strings.Join(out, "\n")
}

var (
	mdHeadingRe = regexp.MustCompile(`^(#{1,6})\s+(.*)$`)
	mdBulletRe  = regexp.MustCompile(`^([ ]*)[-*][ \t]+(.*)$`)
	mdOrderedRe = regexp.MustCompile(`^([ ]*)\d+\.[ \t]+(.*)$`)
	mdQuoteRe   = regexp.MustCompile(`^>[ \t]?(.*)$`)
	mdRuleRe    = regexp.MustCompile(`^(-{3,}|\*{3,})$`)
)

// renderTextLines renders non-code source lines block by block, mirroring
// the browser's line pass: headings, rules, list runs, quotes, blank
// lines, and paragraphs (consecutive plain lines joined with a space, so
// a model that hard-wraps prose at 80 columns is one paragraph, not six).
// Two deliberate additions: indented lines (four spaces or a tab) pass
// through literally, since without fences that indentation is the only
// thing marking code; and table-shaped runs pass through line by line,
// with inline spans applied but never joined or wrapped.
func renderTextLines(lines []string, width int) []string {
	out := []string{}
	var para []string
	closePara := func() {
		if len(para) == 0 {
			return
		}
		joined := strings.Join(para, " ")
		para = nil
		out = append(out, wrapANSI(renderInline(joined), width, ""))
	}
	for i := 0; i < len(lines); i++ {
		line := lines[i]
		trimmed := strings.TrimSpace(line)
		if n := tableRunAt(lines, i); n > 0 {
			closePara()
			for _, row := range lines[i : i+n] {
				if isDelimRow(row) {
					continue
				}
				out = append(out, renderInline(row))
			}
			i += n - 1
			continue
		}
		if m := mdHeadingRe.FindStringSubmatch(line); m != nil {
			closePara()
			// Bold, not nested: an inner span's reset would cancel an
			// outer bold halfway along, so a heading carrying code or
			// emphasis keeps the inner styles on plain text instead.
			out = append(out, mdHeadingStyle.Render(stripANSI(renderInline(m[2]))))
		} else if mdRuleRe.MatchString(trimmed) {
			closePara()
			out = append(out, mdRuleStyle.Render(strings.Repeat("─", width)))
		} else if m := mdBulletRe.FindStringSubmatch(line); m != nil {
			closePara()
			out = append(out, renderListItem(m[1], "• ", m[2], width))
		} else if m := mdOrderedRe.FindStringSubmatch(line); m != nil {
			closePara()
			num := strings.TrimSpace(line[len(m[1]):])
			if cut := strings.IndexAny(num, "."); cut > 0 {
				num = num[:cut+1]
			}
			out = append(out, renderListItem(m[1], num+" ", m[2], width))
		} else if m := mdQuoteRe.FindStringSubmatch(line); m != nil {
			closePara()
			out = append(out, mdQuoteStyle.Render("│ ")+renderInline(m[1]))
		} else if trimmed == "" {
			closePara()
		} else if isIndentedLiteral(line) {
			// Unfenced code: without a fence this indentation is the
			// only thing marking the line as code, so it is kept
			// byte-for-byte instead of joined into a paragraph.
			closePara()
			out = append(out, renderInline(line))
		} else {
			para = append(para, strings.TrimSpace(line))
		}
	}
	closePara()
	return out
}

// renderListItem draws one list item at nesting depth derived from its
// leading spaces (two per level, the browser's flat list being the
// acknowledged divergence). Continuation lines wrap under the text, not
// under the marker.
func renderListItem(indent, marker, text string, width int) string {
	level := len(indent) / 2
	prefix := strings.Repeat("  ", level) + marker
	hanging := strings.Repeat(" ", len(prefix))
	return wrapANSI(prefix+renderInline(text), width, hanging)
}

// isIndentedLiteral reports a line indented as code: four spaces or a
// tab, the CommonMark indented-code threshold the browser never
// implemented but the terminal keeps so unfenced code survives.
func isIndentedLiteral(line string) bool {
	return strings.HasPrefix(line, "    ") || strings.HasPrefix(line, "\t")
}

var mdDelimCellRe = regexp.MustCompile(`^:?-+:?$`)

// tableRunAt returns how many lines starting at i form a table run: a
// header line carrying a pipe followed by a delimiter row, then every
// following line that still carries a pipe. Zero when there is no table
// here, in which case the lines stay ordinary prose. The delimiter test
// mirrors the browser's alignments(): a row whose pipe-separated cells
// are all dashes and optional colons.
func tableRunAt(lines []string, i int) int {
	if i+1 >= len(lines) || !strings.Contains(lines[i], "|") {
		return 0
	}
	if !isDelimRow(lines[i+1]) {
		return 0
	}
	n := i + 2
	for n < len(lines) && strings.Contains(lines[n], "|") {
		n++
	}
	return n - i
}

func isDelimRow(s string) bool {
	cells := strings.Split(s, "|")
	kept := []string{}
	for _, c := range cells {
		if strings.TrimSpace(c) != "" {
			kept = append(kept, c)
		}
	}
	if len(kept) == 0 {
		return false
	}
	for _, c := range kept {
		if !mdDelimCellRe.MatchString(strings.TrimSpace(c)) {
			return false
		}
	}
	return true
}

var (
	// Double-backtick spans may hold single backticks inside (that is
	// their whole point); the content is runs of non-backticks or a
	// lone backtick followed by a non-backtick.
	mdDoubleCodeRe = regexp.MustCompile("``((?:[^`]|`[^`])*?)``")
	mdSingleCodeRe = regexp.MustCompile("`([^`\n]+)`")
	mdStrongStarRe = regexp.MustCompile(`\*\*([^*]+)\*\*`)
	mdEmStarRe     = regexp.MustCompile(`\*([^*]+)\*`)
	// Underscores inside a word are part of the word, not emphasis —
	// CommonMark's rule, and the one that matters in a coding
	// assistant whose prose is full of snake_case. Written with a
	// captured leading character rather than a lookbehind, which RE2
	// does not offer; the trailing boundary is captured rather than
	// looked at, so each pass can eat the space an adjacent emphasis
	// needs and the replacement loops until nothing changes. The
	// classes are Unicode-aware so a Hangul identifier is a word the
	// same way an ASCII one is.
	mdStrongUnderRe = regexp.MustCompile(`(^|[^\p{L}\p{N}_])__([^_]+)__([^\p{L}\p{N}]|$)`)
	mdEmUnderRe     = regexp.MustCompile(`(^|[^\p{L}\p{N}_])_([^_]+)_([^\p{L}\p{N}]|$)`)
	mdLinkRe        = regexp.MustCompile(`\[([^\]]+)\]\((https?://[^\s)]+)\)`)
	mdANSIRe        = regexp.MustCompile("\x1b\\[[0-9;]*m")
	mdSpanTokRe     = regexp.MustCompile("\x00([0-9]+)\x00")
)

// renderInline applies span-level markdown to one block string: code
// spans (held aside first so nothing else touches their contents),
// bold and italic, links, then the spans spliced back styled. The order
// matches the browser: strong before emphasis, links after both, and a
// non-http target stays literal text rather than becoming a link.
func renderInline(s string) string {
	s = unwrapMath(s)
	spans := []string{}
	token := func(i int) string { return "\x00" + strconv.Itoa(i) + "\x00" }
	s = mdDoubleCodeRe.ReplaceAllStringFunc(s, func(m string) string {
		spans = append(spans, mdDoubleCodeRe.FindStringSubmatch(m)[1])
		return token(len(spans) - 1)
	})
	s = mdSingleCodeRe.ReplaceAllStringFunc(s, func(m string) string {
		spans = append(spans, mdSingleCodeRe.FindStringSubmatch(m)[1])
		return token(len(spans) - 1)
	})
	// Styled replacements are built with ReplaceAllStringFunc, never
	// with a $1 template: a style's Render can split its input into
	// several escape runs (underline does), which would mangle a
	// template's group references.
	s = mdStrongStarRe.ReplaceAllStringFunc(s, func(m string) string {
		return mdBoldStyle.Render(mdStrongStarRe.FindStringSubmatch(m)[1])
	})
	for i := 0; i < 10 && mdStrongUnderRe.MatchString(s); i++ {
		s = mdStrongUnderRe.ReplaceAllStringFunc(s, func(m string) string {
			g := mdStrongUnderRe.FindStringSubmatch(m)
			return g[1] + mdBoldStyle.Render(g[2]) + g[3]
		})
	}
	s = mdEmStarRe.ReplaceAllStringFunc(s, func(m string) string {
		return mdItalicStyle.Render(mdEmStarRe.FindStringSubmatch(m)[1])
	})
	for i := 0; i < 10 && mdEmUnderRe.MatchString(s); i++ {
		s = mdEmUnderRe.ReplaceAllStringFunc(s, func(m string) string {
			g := mdEmUnderRe.FindStringSubmatch(m)
			return g[1] + mdItalicStyle.Render(g[2]) + g[3]
		})
	}
	s = mdLinkRe.ReplaceAllStringFunc(s, func(m string) string {
		g := mdLinkRe.FindStringSubmatch(m)
		return mdLinkStyle.Render(g[1]) + " (" + g[2] + ")"
	})
	return mdSpanTokRe.ReplaceAllStringFunc(s, func(m string) string {
		i, err := strconv.Atoi(mdSpanTokRe.FindStringSubmatch(m)[1])
		if err != nil || i < 0 || i >= len(spans) {
			return m
		}
		return mdCodeStyle.Render(spans[i])
	})
}

// stripANSI removes escape sequences for measuring and for contexts
// (headings) where nesting one style inside another would cancel it.
func stripANSI(s string) string {
	return mdANSIRe.ReplaceAllString(s, "")
}

// wrapANSI word-wraps s to width columns without counting escape
// sequences toward the width and without ever splitting a word.
// hanging prefixes continuation lines (a list item's indent). A single
// word wider than width goes on its own line overflowing rather than
// being cut: the viewport clips what the terminal cannot show, while
// bytes the renderer cut could not be recovered by anyone.
func wrapANSI(s string, width int, hanging string) string {
	hangW := lipgloss.Width(hanging)
	words := strings.Fields(s)
	if len(words) == 0 {
		return s
	}
	// Fields drops the first line's own indent (a nested list's two
	// spaces), so it is kept aside and put back on the first line.
	lead := s[:len(s)-len(strings.TrimLeft(s, " \t"))]
	lines := []string{}
	cur := ""
	curW := 0
	for _, w := range words {
		ww := lipgloss.Width(w)
		if cur == "" {
			cur, curW = lead+w, lipgloss.Width(lead)+ww
			lead = ""
			continue
		}
		if curW+1+ww <= width {
			cur += " " + w
			curW += 1 + ww
			continue
		}
		lines = append(lines, cur)
		cur = hanging + w
		curW = hangW + ww
	}
	lines = append(lines, cur)
	return strings.Join(lines, "\n")
}

// isWord reports whether r continues an identifier for keyword lookup.
func isWord(r rune) bool {
	return r == '_' || r == '$' || unicode.IsLetter(r) || unicode.IsDigit(r)
}

// mdKeywords holds the small per-language keyword sets the highlighter
// knows. Deliberately a word list rather than a grammar: the job is to
// pick the control flow out of a block at a glance, not to parse, and a
// wrong guess here only tints a word. Languages without a set still get
// strings, comments and numbers.
var mdKeywords = map[string]map[string]bool{
	"go":         mdWordSet("func package import var const type struct interface map chan if else for range return switch case default break continue go defer select fallthrough goto nil true false new make len cap append panic"),
	"javascript": mdWordSet("const let var function return if else for while class extends new import export from default async await try catch throw switch case break continue typeof instanceof this null undefined true false"),
	"python":     mdWordSet("def class return if elif else for while in not and or is None True False import from as with try except raise lambda pass yield async await"),
	"shell":      mdWordSet("if then else elif fi for while do done case esac function return exit export local echo in"),
	"json":       mdWordSet("true false null"),
}

func mdWordSet(s string) map[string]bool {
	m := map[string]bool{}
	for _, w := range strings.Fields(s) {
		m[w] = true
	}
	return m
}

// mdLangGroup normalises a fence info string to a highlighter language:
// aliases collapse, unknown languages report "" and get the generic
// string/comment/number pass with no keywords.
func mdLangGroup(lang string) string {
	switch strings.ToLower(strings.TrimSpace(lang)) {
	case "go", "golang":
		return "go"
	case "js", "jsx", "ts", "tsx", "typescript", "javascript":
		return "javascript"
	case "py", "python":
		return "python"
	case "sh", "bash", "zsh", "shell":
		return "shell"
	case "json":
		return "json"
	default:
		return ""
	}
}

// highlightCode tints code for the terminal without a dependency: one
// linear scan colouring strings, comments, numbers, and the keywords of
// the languages above. Anything it does not recognise passes through
// untouched, so an unknown language degrades to plain indented code
// rather than to nothing.
func highlightCode(code, lang string) string {
	group := mdLangGroup(lang)
	kw := mdKeywords[group]
	lineComment := ""
	blockComment := false
	quotes := "\"'"
	switch group {
	case "go", "javascript":
		lineComment, blockComment, quotes = "//", true, "\"'`"
	case "python", "shell":
		lineComment, quotes = "#", "\"'"
	case "json":
		quotes = "\""
	}

	var b strings.Builder
	runes := []rune(code)
	i := 0
	flush := func(s string) { b.WriteString(s) }
	for i < len(runes) {
		c := runes[i]
		// Line comments run to the newline.
		if lineComment != "" && c == rune(lineComment[0]) {
			if lineComment == "#" || (i+1 < len(runes) && runes[i+1] == '/') {
				j := i
				for j < len(runes) && runes[j] != '\n' {
					j++
				}
				flush(mdCommentStyle.Render(string(runes[i:j])))
				i = j
				continue
			}
		}
		// Block comments run to the closer, unterminated to the end.
		if blockComment && c == '/' && i+1 < len(runes) && runes[i+1] == '*' {
			j := i + 2
			for j+1 < len(runes) && !(runes[j] == '*' && runes[j+1] == '/') {
				j++
			}
			if j+1 < len(runes) {
				j += 2
			} else {
				j = len(runes)
			}
			flush(mdCommentStyle.Render(string(runes[i:j])))
			i = j
			continue
		}
		// Strings run to the matching quote, honouring backslash
		// escapes everywhere except shell single quotes, where a
		// backslash is a backslash.
		if strings.ContainsRune(quotes, c) {
			raw := c == '\'' && group == "shell"
			j := i + 1
			for j < len(runes) {
				if !raw && runes[j] == '\\' {
					j += 2
					continue
				}
				if runes[j] == c {
					j++
					break
				}
				if runes[j] == '\n' && c != '`' {
					break
				}
				j++
			}
			flush(mdStringStyle.Render(string(runes[i:j])))
			i = j
			continue
		}
		// Numbers start at a digit that does not continue a word.
		if c >= '0' && c <= '9' && (i == 0 || !isWord(runes[i-1])) {
			j := i
			for j < len(runes) && (isWord(runes[j]) || runes[j] == '.') {
				j++
			}
			flush(mdNumberStyle.Render(string(runes[i:j])))
			i = j
			continue
		}
		// Words are keywords only on a full match.
		if isWord(c) && (i == 0 || !isWord(runes[i-1])) {
			j := i
			for j < len(runes) && isWord(runes[j]) {
				j++
			}
			w := string(runes[i:j])
			if kw[w] {
				flush(mdKeywordStyle.Render(w))
			} else {
				flush(w)
			}
			i = j
			continue
		}
		flush(string(c))
		i++
	}
	return b.String()
}

// The LaTeX a model writes for a client that renders it, unwrapped for
// one that does not. Ported from the browser's unwrapMath with the same
// deliberate narrowness: a $ span is touched only when it holds a
// backslash command (a price and $PATH are left alone), and only
// commands with an obvious plain-text equivalent translate, so nothing
// is invented for real formulae — those keep their delimiters.
var mdMathSymbols = map[string]string{
	"rightarrow": "→", "to": "→", "longrightarrow": "→", "Rightarrow": "⇒", "implies": "⇒",
	"leftarrow": "←", "gets": "←", "Leftarrow": "⇐", "leftrightarrow": "↔", "Leftrightarrow": "⇔",
	"uparrow": "↑", "downarrow": "↓", "mapsto": "↦",
	"sim": "∼", "simeq": "≃", "propto": "∝", "cong": "≅",
	"times": "×", "cdot": "·", "div": "÷", "pm": "±", "mp": "∓", "ast": "∗", "star": "⋆",
	"le": "≤", "leq": "≤", "ge": "≥", "geq": "≥", "ne": "≠", "neq": "≠",
	"ll": "≪", "gg": "≫", "approx": "≈", "equiv": "≡",
	"in": "∈", "notin": "∉", "subset": "⊂", "subseteq": "⊆", "supset": "⊃", "supseteq": "⊇",
	"cup": "∪", "cap": "∩", "setminus": "∖", "emptyset": "∅", "varnothing": "∅",
	"forall": "∀", "exists": "∃", "neg": "¬", "land": "∧", "lor": "∨", "therefore": "∴", "because": "∵",
	"infty": "∞", "partial": "∂", "nabla": "∇", "deg": "°", "prime": "′",
	"ldots": "…", "dots": "…", "cdots": "⋯", "bullet": "•", "circ": "∘",
	"perp": "⊥", "parallel": "∥", "angle": "∠", "triangle": "△", "square": "□",
	"alpha": "α", "beta": "β", "gamma": "γ", "delta": "δ", "epsilon": "ε", "varepsilon": "ε",
	"zeta": "ζ", "eta": "η", "theta": "θ", "vartheta": "ϑ", "iota": "ι", "kappa": "κ",
	"lambda": "λ", "mu": "μ", "nu": "ν", "xi": "ξ", "pi": "π", "rho": "ρ", "sigma": "σ",
	"tau": "τ", "upsilon": "υ", "phi": "φ", "varphi": "φ", "chi": "χ", "psi": "ψ", "omega": "ω",
	"Gamma": "Γ", "Delta": "Δ", "Theta": "Θ", "Lambda": "Λ", "Xi": "Ξ", "Pi": "Π",
	"Sigma": "Σ", "Upsilon": "Υ", "Phi": "Φ", "Psi": "Ψ", "Omega": "Ω",
	"quad": " ", "qquad": " ", "thinspace": " ", "space": " ",
}

var (
	mdMathSpanRe  = regexp.MustCompile(`\$\$?([^$\n]+?)\$?\$`)
	mdMathCmdRe   = regexp.MustCompile(`\\([a-zA-Z]+)`)
	mdMathFontRe  = regexp.MustCompile(`\\(?:text|textbf|textit|textrm|textsf|texttt|mathrm|mathbf|mathit|mathsf|mathtt|mathcal|boldsymbol|operatorname)\{([^{}]*)\}`)
	mdMathEscRe   = regexp.MustCompile(`\\([%_])`)
	mdMathSpaceRe = regexp.MustCompile(`\\[,;!:> ]`)
	mdMathMultiRe = regexp.MustCompile(` +`)
)

func unwrapMath(s string) string {
	return mdMathSpanRe.ReplaceAllStringFunc(s, func(m string) string {
		body := mdMathSpanRe.FindStringSubmatch(m)[1]
		if !strings.Contains(body, "\\") {
			return m
		}
		out := body
		for i := 0; i < 8; i++ {
			next := mdMathFontRe.ReplaceAllString(out, "$1")
			if next == out {
				break
			}
			out = next
		}
		out = mdMathCmdRe.ReplaceAllStringFunc(out, func(cmd string) string {
			if sym, ok := mdMathSymbols[cmd[1:]]; ok {
				return sym
			}
			return cmd
		})
		out = mdMathEscRe.ReplaceAllString(out, "$1")
		out = mdMathSpaceRe.ReplaceAllString(out, " ")
		out = mdMathMultiRe.ReplaceAllString(out, " ")
		out = strings.TrimSpace(out)
		if strings.Contains(out, "\\") {
			return m
		}
		return out
	})
}
