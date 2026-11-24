package sshterm

import (
	"encoding/base64"
	"strings"
	"unicode/utf8"

	"github.com/charmbracelet/x/ansi"
)

// A command's output as text, and the marks that say a command ended.

// markScanner finds the shell integration's marks in the shell's output,
// across reads: "OSC 133;C;lc=N" as a command starts and
// "OSC 133;D;status;lc=N" before every prompt (integrate.go). N is the
// terminal's nonce. A mark without it is not ours: anything the remote
// prints can contain an OSC 133, and some shells and terminals emit their
// own, so only one carrying the nonce counts.
type markScanner struct {
	nonce   string
	state   int
	payload []byte
}

const (
	scanText = iota
	scanEsc
	scanOSC
	scanOSCEsc
)

// maxMarkPayload bounds what is kept of an OSC's text: a mark is short
// but for a PowerShell's D mark, which carries its prompt (see
// maxPromptPayload), and an OSC carrying a long title or a clipboard
// payload is not one.
const maxMarkPayload = 4096

// maxPromptPayload bounds the base64 prompt a PowerShell's D mark
// carries, well inside maxMarkPayload with the rest of the mark.
const maxPromptPayload = 3000

// markEvent is a mark: its kind ('C' or 'D'), the index in the read just
// after it, the status a D mark carries, and the prompt a local shell's D
// mark carries too (local.go), as text.
type markEvent struct {
	kind   byte
	end    int
	status string
	prompt string
}

func (s *markScanner) feed(p []byte) []markEvent {
	var out []markEvent
	for i, b := range p {
		switch s.state {
		case scanText:
			if b == 0x1b {
				s.state = scanEsc
			}
		case scanEsc:
			if b == ']' {
				s.state = scanOSC
				s.payload = s.payload[:0]
			} else if b != 0x1b {
				s.state = scanText
			}
		case scanOSC:
			switch b {
			case 0x07:
				if ev, ok := s.parse(); ok {
					ev.end = i + 1
					out = append(out, ev)
				}
				s.state = scanText
			case 0x1b:
				s.state = scanOSCEsc
			default:
				if len(s.payload) < maxMarkPayload {
					s.payload = append(s.payload, b)
				} else {
					s.payload = append(s.payload[:0], 0)
				}
			}
		case scanOSCEsc:
			if b == '\\' {
				if ev, ok := s.parse(); ok {
					ev.end = i + 1
					out = append(out, ev)
				}
				s.state = scanText
			} else if b == ']' {
				s.state = scanOSC
				s.payload = s.payload[:0]
			} else {
				s.state = scanText
			}
		}
	}
	return out
}

func (s *markScanner) parse() (markEvent, bool) {
	if s.nonce == "" {
		return markEvent{}, false
	}
	parts := strings.Split(string(s.payload), ";")
	if len(parts) < 3 || parts[0] != "133" || parts[len(parts)-1] != "lc="+s.nonce {
		return markEvent{}, false
	}
	switch {
	case parts[1] == "C" && len(parts) == 3:
		return markEvent{kind: 'C'}, true
	case parts[1] == "D" && len(parts) == 4:
		return markEvent{kind: 'D', status: parts[2]}, true
	case parts[1] == "D" && len(parts) == 5 && strings.HasPrefix(parts[3], "p="):
		b, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(parts[3], "p="))
		if err != nil || !utf8.Valid(b) {
			return markEvent{kind: 'D', status: parts[2]}, true
		}
		return markEvent{kind: 'D', status: parts[2], prompt: ansi.Strip(strings.ReplaceAll(string(b), "\r", ""))}, true
	}
	return markEvent{}, false
}

// capture keeps a command's output: everything up to max bytes, then the
// first half and the latest half.
type capture struct {
	max     int
	head    []byte
	tail    []byte
	dropped int
}

func (c *capture) write(p []byte) {
	if len(c.head) < c.max/2 {
		n := min(len(p), c.max/2-len(c.head))
		c.head = append(c.head, p[:n]...)
		p = p[n:]
	}
	if len(p) == 0 {
		return
	}
	c.tail = append(c.tail, p...)
	if over := len(c.tail) - c.max/2; over > 0 {
		c.dropped += over
		c.tail = append(c.tail[:0], c.tail[over:]...)
	}
}

// text is the output as text. The shell's echo of the command, the first
// line, is left out when skipEcho.
func (c *capture) text(skipEcho bool) string {
	out := renderText(c.head)
	if c.dropped > 0 || len(c.tail) > 0 {
		tail := renderText(c.tail)
		if c.dropped > 0 {
			out += "\n[... " + itoa(c.dropped) + " bytes of output left out ...]\n"
		}
		out += tail
	}
	if skipEcho {
		if i := strings.IndexByte(out, '\n'); i >= 0 {
			out = out[i+1:]
		} else {
			out = ""
		}
	}
	return strings.TrimRight(out, " \n")
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b[i:])
}

// renderText turns terminal output into plain text: escape sequences
// removed, a carriage return going back to the start of the line (so a
// progress bar leaves its last state), a backspace going back one, an
// erase-to-end-of-line erasing, tabs to the next multiple of eight.
func renderText(p []byte) string {
	var lines []string
	var line []rune
	col := 0
	put := func(r rune) {
		if col < len(line) {
			line[col] = r
		} else {
			for len(line) < col {
				line = append(line, ' ')
			}
			line = append(line, r)
		}
		col++
	}
	for i := 0; i < len(p); {
		b := p[i]
		switch {
		case b == 0x1b:
			i = skipEscape(p, i, func(final byte, params string) {
				if final == 'K' && (params == "" || params == "0") && col < len(line) {
					line = line[:col]
				}
				if final == 'K' && params == "2" {
					line = line[:0]
				}
			})
			continue
		case b == '\n':
			lines = append(lines, strings.TrimRight(string(line), " "))
			line, col = line[:0:0], 0
		case b == '\r':
			col = 0
		case b == '\b':
			if col > 0 {
				col--
			}
		case b == '\t':
			next := (col/8 + 1) * 8
			for col < next {
				put(' ')
			}
		case b < 0x20 || b == 0x7f:
			// Other control characters print nothing.
		default:
			r, size := utf8.DecodeRune(p[i:])
			if r == utf8.RuneError && size <= 1 {
				r = '�'
				size = 1
			}
			put(r)
			i += size
			continue
		}
		i++
	}
	lines = append(lines, strings.TrimRight(string(line), " "))
	return strings.Join(lines, "\n")
}

// skipEscape returns the index after the escape sequence at p[i], and
// tells csi a CSI sequence's final byte and parameters.
func skipEscape(p []byte, i int, csi func(final byte, params string)) int {
	if i+1 >= len(p) {
		return len(p)
	}
	switch p[i+1] {
	case '[':
		j := i + 2
		for j < len(p) && (p[j] < 0x40 || p[j] > 0x7e) {
			j++
		}
		if j < len(p) {
			csi(p[j], string(p[i+2:j]))
			return j + 1
		}
		return len(p)
	case ']', 'P', 'X', '^', '_':
		// Up to BEL or ST.
		for j := i + 2; j < len(p); j++ {
			if p[j] == 0x07 {
				return j + 1
			}
			if p[j] == 0x1b && j+1 < len(p) && p[j+1] == '\\' {
				return j + 2
			}
		}
		return len(p)
	default:
		j := i + 1
		for j < len(p) && p[j] >= 0x20 && p[j] <= 0x2f {
			j++
		}
		return min(j+1, len(p))
	}
}
