package agent

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"localcode/internal/when"
)

// The limits on a repeating booking, as they are typed at a prompt.
//
// A repeat needs somewhere to say when to stop, and the two clients say
// it differently: the dialog has fields, and a command has one string.
// This is the command's half — trailing options, read off the end so
// everything before them is the request.
//
// Trailing rather than leading because the request is the subject and
// reads first: "매일 9시 run the tests --times 10" is a sentence with a
// note after it, and "--times 10 매일 9시 run the tests" is a form.

// scheduleLimits is what the options set.
type scheduleLimits struct {
	stopAt    time.Time
	stopAfter int
	keep      int
	// keepSet distinguishes "--keep 0", which means keep nothing, from
	// the option not being given at all. Zero is a real answer here, so
	// the zero value cannot stand in for its absence.
	keepSet bool
}

// takeLimits reads the trailing options off a request and returns what is
// left of it.
//
// Only at the end, and only as whole words. A prompt that happens to
// contain "--times" in the middle of a sentence is a prompt, not an
// option: the loop stops at the first thing from the back that is not a
// recognised option with a value.
func takeLimits(rest string, now time.Time) (string, scheduleLimits, error) {
	var lim scheduleLimits
	fields := strings.Fields(rest)
	for len(fields) >= 2 {
		name := strings.ToLower(fields[len(fields)-2])
		value := fields[len(fields)-1]
		switch name {
		case "--times", "--count":
			n, err := strconv.Atoi(value)
			if err != nil || n <= 0 {
				return "", lim, fmt.Errorf("%s wants a number of runs, not %q", name, value)
			}
			lim.stopAfter = n
		case "--until":
			// The same parser the booking itself uses, so "--until 내일"
			// and "--until 2026-12-01" both mean what they say and mean
			// it the same way here as everywhere else.
			at, _, err := when.ParseTime(value, now)
			if err != nil {
				return "", lim, fmt.Errorf("--until: %w", err)
			}
			lim.stopAt = at
		case "--keep":
			n, err := strconv.Atoi(value)
			if err != nil || n < -1 {
				return "", lim, fmt.Errorf("--keep wants how many run transcripts to hold on to: -1 for all of them, 0 for none, or a count. Got %q", value)
			}
			lim.keep, lim.keepSet = n, true
		default:
			return strings.Join(fields, " "), lim, nil
		}
		fields = fields[:len(fields)-2]
	}
	return strings.Join(fields, " "), lim, nil
}

// options turns what was typed into what Add takes, and refuses the
// combinations that do not mean anything.
func (lim scheduleLimits) options(rule when.Repeat, at time.Time) (RepeatOptions, error) {
	if !rule.On() {
		// The limits are all about repeating, and silently ignoring them
		// on a booking that runs once would be the wrong kind of quiet:
		// somebody who typed "--times 10" believes they booked ten runs.
		if lim.stopAfter > 0 || !lim.stopAt.IsZero() || lim.keepSet {
			return RepeatOptions{}, fmt.Errorf(
				"--times, --until and --keep are for a repeating booking, and this one runs once. " +
					"Say how often first, e.g. \"매일 9시\", \"every 2 hours\", \"1시간마다\"")
		}
		return RepeatOptions{}, nil
	}
	if !lim.stopAt.IsZero() && !lim.stopAt.After(at) {
		return RepeatOptions{}, fmt.Errorf(
			"--until %s is not after the first run at %s, so nothing would ever run",
			lim.stopAt.Format("2006-01-02 15:04"), at.Format("2006-01-02 15:04"))
	}
	keep := defaultKeep
	if lim.keepSet {
		keep = lim.keep
	}
	return RepeatOptions{
		Rule: rule, StopAt: lim.stopAt, StopAfter: lim.stopAfter, Keep: keep,
	}, nil
}

// describeLimits is the part of the confirmation that says how long this
// goes on for. Said every time a booking repeats, because "until I delete
// it" and "ten times" are the difference between a helper and a thing
// that wakes the machine up all year.
func describeLimits(o RepeatOptions) string {
	if !o.Rule.On() {
		return ""
	}
	var b strings.Builder
	b.WriteString(o.Rule.String())
	switch {
	case o.StopAfter > 0 && !o.StopAt.IsZero():
		fmt.Fprintf(&b, ", %d times or until %s, whichever comes first",
			o.StopAfter, o.StopAt.Format("2006-01-02 15:04"))
	case o.StopAfter > 0:
		fmt.Fprintf(&b, ", %d times", o.StopAfter)
	case !o.StopAt.IsZero():
		fmt.Fprintf(&b, ", until %s", o.StopAt.Format("2006-01-02 15:04"))
	default:
		b.WriteString(", until you delete it")
	}
	switch {
	case o.Keep < 0:
		b.WriteString(". Every run's transcript is kept")
	case o.Keep == 0:
		b.WriteString(". No run transcripts are kept — the row still says whether each run worked")
	default:
		fmt.Fprintf(&b, ". The last %d runs' transcripts are kept", o.Keep)
	}
	fmt.Fprintf(&b, ", and it stops itself if %d runs in a row fail", maxConsecutiveFailures)
	return b.String()
}

// BuildRepeatOptions is the HTTP shape of the same limits: fields rather
// than trailing words, since the dialog has boxes for them.
//
// keep is a pointer because 0 is a real answer — keep nothing — and has
// to be told apart from the field being absent, which means the default.
func BuildRepeatOptions(rule when.Repeat, at, now time.Time, times int, until string, keep *int) (RepeatOptions, error) {
	var lim scheduleLimits
	lim.stopAfter = times
	if strings.TrimSpace(until) != "" {
		stop, _, err := when.ParseTime(until, now)
		if err != nil {
			return RepeatOptions{}, fmt.Errorf("until: %w", err)
		}
		lim.stopAt = stop
	}
	if keep != nil {
		lim.keep, lim.keepSet = *keep, true
	}
	return lim.options(rule, at)
}

// DescribeRepeat is describeLimits for a caller outside this package —
// the endpoint echoes the commitment back the way the command does.
func DescribeRepeat(o RepeatOptions) string { return describeLimits(o) }
