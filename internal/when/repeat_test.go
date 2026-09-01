package when

import (
	"strings"
	"testing"
	"time"
)

// A repeating request is a rule plus a first moment, and both halves have
// to come out of one string. The vocabulary was already here — it was
// recognised only in order to refuse it — so what these pin is that it
// now reads as what it says.
func TestARepeatIsReadAsARuleAndAFirstRun(t *testing.T) {
	now := time.Date(2026, 9, 1, 14, 0, 0, 0, time.UTC) // a Tuesday
	for _, tt := range []struct {
		in    string
		rule  string
		first string
		rest  string
	}{
		{"매일 9시 run the tests", "every day", "2026-09-01 21:00", "run the tests"},
		{"every day at 9am summarize", "every day", "2026-09-02 09:00", "summarize"},
		{"매주 월요일 저녁 do the thing", "every week", "2026-09-07 18:00", "do the thing"},
		{"1시간마다 check the build", "every hour", "2026-09-01 15:00", "check the build"},
		{"30분마다 poll it", "every 30 minutes", "2026-09-01 14:30", "poll it"},
		{"every 2 hours check the build", "every 2 hours", "2026-09-01 16:00", "check the build"},
		{"매시간 ping", "every hour", "2026-09-01 15:00", "ping"},
		{"hourly ping", "every hour", "2026-09-01 15:00", "ping"},
		{"2일마다 tidy up", "every 2 days", "2026-09-03 14:00", "tidy up"},
	} {
		at, rep, rest, err := Parse(tt.in, now)
		if err != nil {
			t.Errorf("Parse(%q): %v", tt.in, err)
			continue
		}
		if rep.String() != tt.rule {
			t.Errorf("Parse(%q) rule = %q, want %q", tt.in, rep.String(), tt.rule)
		}
		if got := at.Format("2006-01-02 15:04"); got != tt.first {
			t.Errorf("Parse(%q) first run = %s, want %s", tt.in, got, tt.first)
		}
		if rest != tt.rest {
			t.Errorf("Parse(%q) request = %q, want %q", tt.in, rest, tt.rest)
		}
	}
}

// The ordinary booking is untouched, and that is most of them.
func TestASingleBookingHasNoRule(t *testing.T) {
	now := time.Date(2026, 9, 1, 14, 0, 0, 0, time.UTC)
	_, rep, rest, err := Parse("30분 뒤 run the tests", now)
	if err != nil {
		t.Fatal(err)
	}
	if rep.On() {
		t.Errorf("a one-off booking came back with the rule %q", rep.String())
	}
	if rest != "run the tests" {
		t.Errorf("request = %q", rest)
	}
}

// A repeat with no time of day in it starts one step from now, for every
// unit alike.
//
// That is a reading rather than a guess, which is the distinction this
// package is built around: picking nine in the morning for "매일" would be
// the guess, and starting a day from now at the time you asked is not.
// The confirmation echoes the first run back, so it is visible before
// anything is booked.
func TestARepeatWithNoTimeOfDayStartsOneStepFromNow(t *testing.T) {
	now := time.Date(2026, 9, 1, 14, 0, 0, 0, time.UTC)
	for _, tt := range []struct{ in, first string }{
		{"매일 run the tests", "2026-09-02 14:00"},
		{"매시간 run the tests", "2026-09-01 15:00"},
		{"매주 run the tests", "2026-09-08 14:00"},
		{"2일마다 tidy up", "2026-09-03 14:00"},
	} {
		at, rep, _, err := Parse(tt.in, now)
		if err != nil {
			t.Errorf("Parse(%q): %v", tt.in, err)
			continue
		}
		if got := at.Format("2006-01-02 15:04"); got != tt.first {
			t.Errorf("Parse(%q) first run = %s, want %s (rule %q)", tt.in, got, tt.first, rep)
		}
	}
}

// Two shapes that have no reading which is not a guess.
func TestTheRepeatsThatAreRefused(t *testing.T) {
	now := time.Date(2026, 9, 1, 14, 0, 0, 0, time.UTC)
	for _, tt := range []struct{ in, want string }{
		// A month is not a fixed length and the 31st is not in every one.
		{"매달 1일 report", "by the month"},
		{"monthly report", "by the month"},
		// A run is a whole session and a model call.
		{"30초마다 poll", "Use minutes or longer"},
	} {
		_, _, _, err := Parse(tt.in, now)
		if err == nil {
			t.Errorf("Parse(%q) was accepted", tt.in)
			continue
		}
		if !strings.Contains(err.Error(), tt.want) {
			t.Errorf("Parse(%q) said %q, want it to mention %q", tt.in, err, tt.want)
		}
	}
}

// A day is not 24 hours twice a year, which is the whole reason the rule
// is a step and a unit rather than a duration. Asia/Seoul has no daylight
// saving, so this uses a zone that does.
func TestADailyRepeatKeepsItsClockTimeAcrossDaylightSaving(t *testing.T) {
	ny, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Skip("no tzdata")
	}
	// 2026-11-01 is when the US clocks go back.
	at := time.Date(2026, 10, 31, 9, 0, 0, 0, ny)
	daily := Repeat{1, UnitDay}

	next := daily.Next(at, at)
	if h := next.Hour(); h != 9 {
		t.Errorf("the day after = %s, want it still at 9am", next)
	}
	after := daily.Next(next, next)
	if h := after.Hour(); h != 9 {
		t.Errorf("across the boundary = %s, want it still at 9am — a duration would have walked it to 8", after)
	}
	// And an hourly rule really is an hour, which is the other half of
	// the same distinction.
	hourly := Repeat{1, UnitHour}
	start := time.Date(2026, 11, 1, 0, 30, 0, 0, ny)
	if d := hourly.Next(start, start).Sub(start); d != time.Hour {
		t.Errorf("an hour stepped %v, want exactly an hour", d)
	}
}

// Next has to land in the future however long the gap is, or a booking
// restored after an outage is armed for a moment that has gone.
func TestNextLandsAfterALongOutage(t *testing.T) {
	start := time.Date(2026, 9, 1, 9, 0, 0, 0, time.UTC)
	hourly := Repeat{1, UnitHour}

	// Three days of the machine being off.
	back := start.AddDate(0, 0, 3)
	next := hourly.Next(start, back)
	if !next.After(back) {
		t.Fatalf("next = %s, which is not after %s", next, back)
	}
	// And it is the very next slot, not three days of them at once: the
	// caller fires one run, and a booking that fired 72 times to catch up
	// would be doing work nobody asked for at moments they did not choose.
	if d := next.Sub(back); d > time.Hour {
		t.Errorf("next is %v after the outage, want the next slot within one step", d)
	}
	// The series keeps its minute rather than drifting onto the outage's.
	if next.Minute() != 0 {
		t.Errorf("next = %s, want it still on the hour", next)
	}
}

func TestTheRuleReadsBackInWords(t *testing.T) {
	for _, tt := range []struct {
		r    Repeat
		want string
	}{
		{Repeat{}, ""},
		{Repeat{1, UnitDay}, "every day"},
		{Repeat{1, UnitHour}, "every hour"},
		{Repeat{2, UnitHour}, "every 2 hours"},
		{Repeat{30, UnitMinute}, "every 30 minutes"},
		{Repeat{3, UnitWeek}, "every 3 weeks"},
	} {
		if got := tt.r.String(); got != tt.want {
			t.Errorf("%+v = %q, want %q", tt.r, got, tt.want)
		}
	}
}

// The When field on its own carries the rule too, so the dialog can echo
// it back before anything is booked.
func TestTheWhenFieldAloneCarriesTheRule(t *testing.T) {
	now := time.Date(2026, 9, 1, 14, 0, 0, 0, time.UTC)
	at, rep, err := ParseTime("매일 9시", now)
	if err != nil {
		t.Fatal(err)
	}
	if rep.String() != "every day" {
		t.Errorf("rule = %q", rep.String())
	}
	if got := at.Format("15:04"); got != "21:00" {
		t.Errorf("first run = %s", got)
	}
}
