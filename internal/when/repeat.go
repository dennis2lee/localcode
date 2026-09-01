package when

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// How often a booking runs again.
//
// Repeats were refused for a long time, and the refusal named its own
// price: "a repeat needs a stop condition and a policy for what happens
// when it fails, and neither exists yet." Both exist now — see
// internal/agent/schedule.go for the stop conditions and the suspension
// — so this turns the vocabulary that was only ever recognised in order
// to say no into something that parses.

// Repeat is the step between one run and the next. The zero value is a
// booking that runs once, which is most of them.
//
// A step and a unit rather than a time.Duration, because a day is not
// 24 hours twice a year. "매일 9시" across a daylight-saving boundary is
// 23 hours or 25, and a duration would walk the booking an hour off the
// time somebody asked for and keep walking. Next does calendar
// arithmetic for the calendar units and clock arithmetic for the rest,
// which is the difference between "every day at nine" and "every 24
// hours".
type Repeat struct {
	Every int    `json:"every,omitempty"`
	Unit  string `json:"unit,omitempty"`
}

// Repeat units. Months are deliberately absent — see monthRefusal.
const (
	UnitMinute = "minute"
	UnitHour   = "hour"
	UnitDay    = "day"
	UnitWeek   = "week"
)

// On reports whether this is a repeat at all.
func (r Repeat) On() bool {
	return r.Every > 0 && r.Unit != ""
}

// Next is the occurrence after `from` that is also after `after`.
//
// Always at least one step past `from`, whatever `after` says. `from` is
// the occurrence that has just run, and a rule that could return it again
// would arm the booking for a moment it has already served — which is not
// a repeat but a loop. Getting this wrong is invisible in production,
// where the run takes long enough that the clock has moved past `from`
// on its own; it shows up the moment anything fires a booking early.
//
// Stepping from the previous occurrence rather than from now, so a run
// that starts late does not drag the whole series late with it. The loop
// is what keeps a long outage from leaving the booking in the past.
func (r Repeat) Next(from, after time.Time) time.Time {
	if !r.On() {
		return time.Time{}
	}
	at := r.step(from)
	for i := 0; i < maxCatchUpSteps && !at.After(after); i++ {
		at = r.step(at)
	}
	if !at.After(after) {
		// A very long outage against a very short step. Landing on the
		// next whole step from `after` keeps the clock time of the
		// series for the calendar units and is simply the next slot for
		// the others; either way the booking is in the future, which is
		// the only property anything downstream depends on.
		return r.step(after)
	}
	return at
}

// maxCatchUpSteps bounds the walk in Next. A minute-by-minute booking
// left through a fortnight's outage is twenty thousand steps of integer
// arithmetic, which is nothing, but an unbounded loop over a rule that
// somehow fails to advance is a hang.
const maxCatchUpSteps = 100000

func (r Repeat) step(at time.Time) time.Time {
	switch r.Unit {
	case UnitMinute:
		return at.Add(time.Duration(r.Every) * time.Minute)
	case UnitHour:
		return at.Add(time.Duration(r.Every) * time.Hour)
	case UnitDay:
		return at.AddDate(0, 0, r.Every)
	case UnitWeek:
		return at.AddDate(0, 0, 7*r.Every)
	}
	return at
}

// String is the rule in the words the confirmation echoes back, so a
// misread repeat is caught before it is booked rather than at the third
// unexpected run.
func (r Repeat) String() string {
	if !r.On() {
		return ""
	}
	unit := r.Unit
	if r.Every != 1 {
		unit += "s"
	}
	if r.Every == 1 {
		return "every " + r.Unit
	}
	return fmt.Sprintf("every %d %s", r.Every, unit)
}

// repeatPrefixes are the leading words that open a repeating request and
// the rule each one names. Order matters: the longest spelling of a
// prefix has to be tried before a shorter one it starts with, or "매주"
// never matches because "매" is not on the list but "매시간" and "매주"
// both begin the same way in a byte comparison against "매주말".
var repeatPrefixes = []struct {
	word string
	rule Repeat
}{
	{"매시간", Repeat{1, UnitHour}},
	{"매분", Repeat{1, UnitMinute}},
	{"매일", Repeat{1, UnitDay}},
	{"매주", Repeat{1, UnitWeek}},
	{"날마다", Repeat{1, UnitDay}},
	{"주마다", Repeat{1, UnitWeek}},
	{"hourly", Repeat{1, UnitHour}},
	{"daily", Repeat{1, UnitDay}},
	{"weekly", Repeat{1, UnitWeek}},
	{"each day", Repeat{1, UnitDay}},
	{"each week", Repeat{1, UnitWeek}},
}

// monthWords are refused rather than supported. A month is not a fixed
// length and the 31st does not exist in every one of them, so "매달 31일"
// has no answer that is not somebody's guess — and this package guesses
// nowhere else.
var monthWords = []string{"매달", "매월", "monthly", "each month"}

func monthRefusal(word string) error {
	return fmt.Errorf(
		"%q repeats by the month, which localcode does not book: a month is not a fixed length "+
			"and the 31st does not exist in every one of them, so there is no reading of it that is "+
			"not a guess. Repeat by the week or the day instead — \"매주 월요일 9시\", \"every 30 days 9am\"", word)
}

// everyRe reads "every 2 hours", "every hour", "every 30 minutes". The
// count is optional because "every hour" is how people say one of them.
var everyRe = regexp.MustCompile(`^(?i)(every|repeat every)\s+(?:(\d+)\s*)?([a-z가-힣]+)`)

// markerRe reads the shape the count comes first in: "1시간마다",
// "30분마다", "2일마다". The marker is what used to be left over when the
// old parser read the amount as a one-off time and then found "마다"
// behind it — which booked a single run at the wrong moment.
var markerRe = regexp.MustCompile(`^(\d+)\s*(초|분|시간|일|주)\s*(?:간)?마다`)

// TakeRepeat pulls a repeat rule off the front of s and returns what is
// left for the ordinary time parsing to read as the first occurrence.
//
// A zero Repeat and s unchanged when there is no repeat in it, which is
// the common case and costs one failed prefix match.
func TakeRepeat(s string) (Repeat, string, error) {
	t := strings.TrimSpace(s)
	if word, ok := leadingWord(t, monthWords); ok {
		return Repeat{}, "", monthRefusal(word)
	}
	for _, p := range repeatPrefixes {
		if len(t) >= len(p.word) && strings.EqualFold(t[:len(p.word)], p.word) {
			return p.rule, strings.TrimSpace(t[len(p.word):]), nil
		}
	}
	if m := markerRe.FindStringSubmatch(t); m != nil {
		rule, err := ruleFor(m[1], m[2])
		if err != nil {
			return Repeat{}, "", err
		}
		return rule, strings.TrimSpace(t[len(m[0]):]), nil
	}
	if m := everyRe.FindStringSubmatch(t); m != nil {
		count := m[2]
		if count == "" {
			count = "1"
		}
		rule, err := ruleFor(count, m[3])
		if err != nil {
			return Repeat{}, "", err
		}
		return rule, strings.TrimSpace(t[len(m[0]):]), nil
	}
	return Repeat{}, s, nil
}

// ruleFor turns a count and a unit word into a rule, or says why not.
func ruleFor(count, unit string) (Repeat, error) {
	n, err := strconv.Atoi(count)
	if err != nil || n <= 0 {
		return Repeat{}, fmt.Errorf("%q is not a number of times to repeat", count)
	}
	switch strings.ToLower(strings.TrimSuffix(strings.ToLower(unit), "s")) {
	case "분", "min", "minute":
		return Repeat{n, UnitMinute}, nil
	case "시간", "hour", "hr":
		return Repeat{n, UnitHour}, nil
	case "일", "day":
		return Repeat{n, UnitDay}, nil
	case "주", "week":
		return Repeat{n, UnitWeek}, nil
	case "초", "second", "sec":
		// Refused rather than supported. A booking every few seconds is
		// a loop, not a schedule: each run is a session and a model call,
		// and the shortest thing worth booking is minutes.
		return Repeat{}, fmt.Errorf("repeating by the second is too often to book — a run is a whole session and a model call. Use minutes or longer")
	case "달", "월", "month":
		return Repeat{}, monthRefusal(unit)
	}
	return Repeat{}, fmt.Errorf("%q is not a unit to repeat by. Use minutes, hours, days or weeks", unit)
}
