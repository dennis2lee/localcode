// Package when turns the front of a sentence into a time.
//
// "/schedule 30분 뒤 run the tests" has to split into a moment and a
// request, and the split is the whole difficulty: the text is one string
// and only the person typing knows where the time ends.
//
// This is deliberately not the model's job. A local model asked for a
// timestamp gets the year wrong occasionally, and a scheduled task is
// exactly the place where an occasional wrong answer is invisible until
// the day it matters. Parsing here is a small amount of code that is
// testable, says no clearly, and is the same on every machine and every
// model. The caller echoes what was parsed back, so a wrong reading is
// caught before the work is booked rather than after it fails to happen.
package when

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Parse reads a leading time expression and returns the moment it names
// and the rest of the text.
//
// now is passed in rather than read, so a test can ask what "tomorrow
// morning" means without waiting for tomorrow.
//
// Two rules decide the awkward cases, and both are the reading a person
// means:
//
//   - A clock time that has already gone today is tomorrow. Typing "3시에"
//     at four in the afternoon means the next three o'clock; booking a
//     moment in the past would be a request nothing can honour.
//   - A bare time with no request after it is not a schedule. There is
//     nothing to run, and reporting that is better than booking silence.
func Parse(text string, now time.Time) (at time.Time, rest string, err error) {
	s := strings.TrimSpace(text)
	if s == "" {
		return time.Time{}, "", fmt.Errorf("say when, and what to do: %s", Examples)
	}

	for _, try := range []func(string, time.Time) (time.Time, string, bool){
		parseAbsolute,
		parseRelative,
		parseDayAndClock,
	} {
		if at, rest, ok := try(s, now); ok {
			rest = strings.TrimSpace(rest)
			if rest == "" {
				return time.Time{}, "", fmt.Errorf("that says when but not what to do. Add the request after the time, e.g. %q", "30분 뒤 run the tests")
			}
			if !at.After(now) {
				// Only reachable for an absolute date in the past, since
				// the clock forms roll forward on their own.
				return time.Time{}, "", fmt.Errorf("%s is in the past", at.Format("2006-01-02 15:04"))
			}
			return at, rest, nil
		}
	}
	return time.Time{}, "", fmt.Errorf("could not read a time from %q. %s", firstWords(s, 4), Examples)
}

// Examples is the one line shown whenever a time could not be read. It is
// the whole of the documentation most people will ever look at, so it
// carries both languages and all three shapes: relative, clock, and date.
const Examples = `Try "30분 뒤", "in 2 hours", "오후 3시", "at 3pm", "내일 아침", "tomorrow 9am", or "2026-09-01 14:30".`

func firstWords(s string, n int) string {
	f := strings.Fields(s)
	if len(f) > n {
		f = f[:n]
	}
	return strings.Join(f, " ")
}

// ---- absolute ----

var absoluteRe = regexp.MustCompile(`^(\d{4})-(\d{2})-(\d{2})(?:[ T](\d{1,2}):(\d{2}))?\b`)

// parseAbsolute reads "2026-09-01" or "2026-09-01 14:30". A date with no
// clock means nine in the morning rather than midnight: a date typed
// without a time means "that day", and the start of a working day is a
// better guess at what somebody meant by it than the first second of it.
func parseAbsolute(s string, now time.Time) (time.Time, string, bool) {
	m := absoluteRe.FindStringSubmatch(s)
	if m == nil {
		return time.Time{}, "", false
	}
	year, _ := strconv.Atoi(m[1])
	month, _ := strconv.Atoi(m[2])
	day, _ := strconv.Atoi(m[3])
	hour, minute := 9, 0
	if m[4] != "" {
		hour, _ = strconv.Atoi(m[4])
		minute, _ = strconv.Atoi(m[5])
	}
	if month < 1 || month > 12 || day < 1 || day > 31 || hour > 23 || minute > 59 {
		return time.Time{}, "", false
	}
	at := time.Date(year, time.Month(month), day, hour, minute, 0, 0, now.Location())
	return at, s[len(m[0]):], true
}

// ---- relative ----

// Longest unit first, and a real separator at the end rather than \b:
// RE2's \b is ASCII-only, so there is no word boundary after "분" or
// "시간" and the pattern simply never matched the Korean forms it was
// written for. The clock parser then caught "2시간 뒤" as two o'clock.
var relativeRe = regexp.MustCompile(`^(?i)(?:in|after)?\s*(\d+)\s*(초|분|시간|일|seconds|second|secs|sec|minutes|minute|mins|min|hours|hour|hrs|hr|days|day|s|m|h|d)\s*(?:뒤|후|이따가|later|from now)?(?:\s+|$)`)

// parseRelative reads "30분 뒤", "in 2 hours", "2h", "3일 후".
//
// The trailing "뒤"/"후"/"later" is optional because "in 2 hours" does not
// have one and "2시간" on its own is unambiguous, and the leading "in" is
// optional for the same reason from the other side.
func parseRelative(s string, now time.Time) (time.Time, string, bool) {
	m := relativeRe.FindStringSubmatch(s)
	if m == nil {
		return time.Time{}, "", false
	}
	n, err := strconv.Atoi(m[1])
	if err != nil || n <= 0 {
		return time.Time{}, "", false
	}
	var d time.Duration
	switch strings.ToLower(m[2]) {
	case "초", "s", "sec", "secs", "second", "seconds":
		d = time.Duration(n) * time.Second
	case "분", "m", "min", "mins", "minute", "minutes":
		d = time.Duration(n) * time.Minute
	case "시간", "h", "hr", "hrs", "hour", "hours":
		d = time.Duration(n) * time.Hour
	case "일", "d", "day", "days":
		d = time.Duration(n) * 24 * time.Hour
	default:
		return time.Time{}, "", false
	}
	return now.Add(d).Truncate(time.Second), s[len(m[0]):], true
}

// ---- a day, and a clock on it ----

// dayWords move the date. Anything else leaves it today, and the clock
// decides whether "today" turns out to mean tomorrow.
var dayWords = []struct {
	word string
	days int
}{
	{"내일모레", 2}, {"모레", 2}, {"day after tomorrow", 2},
	{"내일", 1}, {"tomorrow", 1}, {"tmr", 1},
	{"오늘", 0}, {"today", 0}, {"tonight", 0}, {"이따", 0},
}

// namedTimes are the parts of a day people name instead of a number. The
// hours are the ordinary reading of each word, and they exist because
// "내일 아침" is a far more common way to say nine in the morning than
// "내일 오전 9시" is.
var namedTimes = []struct {
	word string
	hour int
}{
	{"아침", 9}, {"morning", 9},
	{"점심", 12}, {"정오", 12}, {"noon", 12}, {"lunchtime", 12}, {"lunch", 12},
	{"오후", 15}, {"afternoon", 15},
	{"저녁", 18}, {"evening", 18},
	{"밤", 21}, {"tonight", 21}, {"night", 21},
	{"새벽", 5}, {"dawn", 5},
}

var (
	koreanClockRe  = regexp.MustCompile(`^\s*(오전|오후)?\s*(\d{1,2})\s*시\s*(?:(\d{1,2})\s*분)?\s*(?:에)?`)
	westernClockRe = regexp.MustCompile(`^(?i)\s*(?:at\s+)?(\d{1,2})(?::(\d{2}))?\s*(am|pm)?\b`)
)

// parseDayAndClock reads an optional day word, then an optional named
// time or clock: "내일 아침", "tomorrow 9am", "오후 3시 30분", "at 15:00",
// "tonight".
//
// Written as one function because the two halves are not independent —
// "내일" alone is a day with no time, "9am" alone is a time with no day,
// and each fills in what the other left out.
func parseDayAndClock(s string, now time.Time) (time.Time, string, bool) {
	rest := s
	days, sawDay := 0, false
	for _, d := range dayWords {
		if cut, ok := cutPrefixFold(rest, d.word); ok {
			days, sawDay, rest = d.days, true, cut
			break
		}
	}

	// The clock first, because it carries its own 오전/오후 and a named
	// part of the day would otherwise swallow the half of "오후 3시" that
	// says three: the named list matched "오후", and "3시 deploy" became
	// the request.
	if at, cut, matched, ok := parseKoreanClock(rest, now, days, sawDay, -1); matched {
		return at, cut, ok
	}

	// A named part of the day: "아침", "evening", "저녁 7시". The hour it
	// names is used on its own, and as the am/pm sense of a bare clock
	// after it, which is how the pair is actually said.
	for _, n := range namedTimes {
		cut, ok := cutPrefixFold(rest, n.word)
		if !ok {
			continue
		}
		if at, c2, matched, valid := parseKoreanClock(cut, now, days, sawDay, n.hour); matched {
			return at, c2, valid
		}
		if at, c2, matched, valid := parseWesternClock(cut, now, days, sawDay, n.hour); matched {
			return at, c2, valid
		}
		return rollForward(now, days, n.hour, 0, sawDay), cut, true
	}

	if at, cut, matched, ok := parseWesternClock(rest, now, days, sawDay, -1); matched {
		return at, cut, ok
	}

	// A day word with no time at all: "내일 run the tests" means nine in
	// the morning, the same reading a bare date gets.
	if sawDay {
		return rollForward(now, days, 9, 0, true), rest, true
	}
	return time.Time{}, "", false
}

// applyHint gives a bare clock the am/pm sense of the word in front of
// it: "저녁 7시" is seven in the evening, and "아침 9시" is nine in the
// morning. hint is the named hour, or -1 when there was no such word.
func applyHint(hour, hint int) int {
	if hint < 0 || hour > 12 {
		return hour
	}
	if hint >= 12 && hour < 12 {
		return hour + 12
	}
	if hint < 12 && hour == 12 {
		return 0
	}
	return hour
}

func parseKoreanClock(rest string, now time.Time, days int, sawDay bool, hint int) (at time.Time, cut string, matched, valid bool) {
	m := koreanClockRe.FindStringSubmatch(rest)
	if m == nil || m[2] == "" {
		return time.Time{}, "", false, false
	}
	hour, _ := strconv.Atoi(m[2])
	minute := 0
	if m[3] != "" {
		minute, _ = strconv.Atoi(m[3])
	}
	switch m[1] {
	case "오후":
		if hour < 12 {
			hour += 12
		}
	case "오전":
		if hour == 12 {
			hour = 0
		}
	default:
		hour = applyHint(hour, hint)
	}
	if hour > 23 || minute > 59 {
		// Matched the shape of a time and is not one. Reported as a
		// failed parse rather than as "no time here", so "오후 25시
		// deploy" is refused instead of being booked for three in the
		// afternoon with "25시 deploy" as the request.
		return time.Time{}, "", true, false
	}
	return rollForward(now, days, hour, minute, sawDay), rest[len(m[0]):], true, true
}

func parseWesternClock(rest string, now time.Time, days int, sawDay bool, hint int) (at time.Time, cut string, matched, valid bool) {
	m := westernClockRe.FindStringSubmatch(rest)
	// A bare number with no colon and no am/pm is not a time: "3 run the
	// tests" is far more likely to be a typo than a request for three
	// o'clock, and reading it as one books work at a moment nobody chose.
	if m == nil || (m[2] == "" && m[3] == "" && hint < 0) {
		return time.Time{}, "", false, false
	}
	hour, _ := strconv.Atoi(m[1])
	minute := 0
	if m[2] != "" {
		minute, _ = strconv.Atoi(m[2])
	}
	switch strings.ToLower(m[3]) {
	case "pm":
		if hour < 12 {
			hour += 12
		}
	case "am":
		if hour == 12 {
			hour = 0
		}
	default:
		hour = applyHint(hour, hint)
	}
	if hour > 23 || minute > 59 {
		return time.Time{}, "", true, false
	}
	return rollForward(now, days, hour, minute, sawDay), rest[len(m[0]):], true, true
}

// rollForward builds the moment, and moves it to tomorrow when the clock
// has already gone today and no day was named.
//
// The exception is what makes it right: "내일 아침" with days=1 must stay
// on tomorrow whatever the current time is, and only a time with no day
// attached is allowed to roll.
func rollForward(now time.Time, days, hour, minute int, sawDay bool) time.Time {
	at := time.Date(now.Year(), now.Month(), now.Day()+days, hour, minute, 0, 0, now.Location())
	if !sawDay && !at.After(now) {
		at = at.AddDate(0, 0, 1)
	}
	return at
}

// cutPrefixFold is strings.CutPrefix, case-insensitively, after trimming
// the leading space. It returns the remainder with one leading space
// eaten so the next parser starts where a reader would.
func cutPrefixFold(s, prefix string) (string, bool) {
	t := strings.TrimLeft(s, " \t")
	if len(t) < len(prefix) || !strings.EqualFold(t[:len(prefix)], prefix) {
		return "", false
	}
	return t[len(prefix):], true
}

// Format is how a parsed time is echoed back for confirmation, which is
// the whole reason parsing is allowed to guess at all: a wrong reading is
// caught here, before the work is booked, instead of by the work not
// happening.
func Format(at, now time.Time) string {
	d := at.Sub(now)
	switch {
	case d < time.Minute:
		return at.Format("15:04:05") + " (in under a minute)"
	case d < time.Hour:
		return fmt.Sprintf("%s (in %d minutes)", at.Format("15:04"), int(d.Minutes()))
	case sameDay(at, now):
		return fmt.Sprintf("%s today (in %s)", at.Format("15:04"), roughly(d))
	case sameDay(at, now.AddDate(0, 0, 1)):
		return fmt.Sprintf("%s tomorrow (in %s)", at.Format("15:04"), roughly(d))
	default:
		return fmt.Sprintf("%s (in %s)", at.Format("2006-01-02 15:04"), roughly(d))
	}
}

func sameDay(a, b time.Time) bool {
	y1, m1, d1 := a.Date()
	y2, m2, d2 := b.Date()
	return y1 == y2 && m1 == m2 && d1 == d2
}

func roughly(d time.Duration) string {
	switch {
	case d < time.Hour:
		return fmt.Sprintf("%d minutes", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%.1f hours", d.Hours())
	default:
		return fmt.Sprintf("%.1f days", d.Hours()/24)
	}
}
