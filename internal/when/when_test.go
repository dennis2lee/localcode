package when

import (
	"strings"
	"testing"
	"time"
)

// A fixed Wednesday afternoon, so "3시" is behind us and "저녁" is not.
var now = time.Date(2026, 8, 26, 16, 30, 0, 0, time.UTC)

func TestParse(t *testing.T) {
	for _, tt := range []struct {
		in     string
		wantAt time.Time
		want   string
	}{
		// Relative, both languages, with and without the trailing word.
		{"30분 뒤 run the tests", now.Add(30 * time.Minute), "run the tests"},
		{"30분 후 run the tests", now.Add(30 * time.Minute), "run the tests"},
		{"2시간 뒤 check the build", now.Add(2 * time.Hour), "check the build"},
		{"in 45 minutes summarize the diff", now.Add(45 * time.Minute), "summarize the diff"},
		{"in 2h check the build", now.Add(2 * time.Hour), "check the build"},
		{"3일 후 review this", now.Add(72 * time.Hour), "review this"},

		// A clock that has already gone today is tomorrow. This is the
		// rule that decides most of the awkward cases.
		{"오후 3시 deploy", time.Date(2026, 8, 27, 15, 0, 0, 0, time.UTC), "deploy"},
		{"at 3pm deploy", time.Date(2026, 8, 27, 15, 0, 0, 0, time.UTC), "deploy"},
		// One that has not.
		{"오후 6시 30분 deploy", time.Date(2026, 8, 26, 18, 30, 0, 0, time.UTC), "deploy"},
		{"18:00 deploy", time.Date(2026, 8, 26, 18, 0, 0, 0, time.UTC), "deploy"},

		// A named day pins the date, so the roll-forward must not apply.
		{"내일 아침 9시 stand-up notes", time.Date(2026, 8, 27, 9, 0, 0, 0, time.UTC), "stand-up notes"},
		{"내일 아침 stand-up notes", time.Date(2026, 8, 27, 9, 0, 0, 0, time.UTC), "stand-up notes"},
		{"tomorrow 9am stand-up notes", time.Date(2026, 8, 27, 9, 0, 0, 0, time.UTC), "stand-up notes"},
		{"내일 저녁 write the summary", time.Date(2026, 8, 27, 18, 0, 0, 0, time.UTC), "write the summary"},
		{"모레 점심 check in", time.Date(2026, 8, 28, 12, 0, 0, 0, time.UTC), "check in"},
		// A day with no time at all lands at nine.
		{"내일 run the tests", time.Date(2026, 8, 27, 9, 0, 0, 0, time.UTC), "run the tests"},
		// A part of the day with no day named, still ahead of us today.
		{"저녁 write the summary", time.Date(2026, 8, 26, 18, 0, 0, 0, time.UTC), "write the summary"},
		// ... and one behind us rolls.
		{"아침 write the summary", time.Date(2026, 8, 27, 9, 0, 0, 0, time.UTC), "write the summary"},

		// A named part of the day gives a bare clock its am/pm sense,
		// which is how the pair is actually said.
		{"저녁 7시 deploy", time.Date(2026, 8, 26, 19, 0, 0, 0, time.UTC), "deploy"},
		{"내일 아침 8시 30분 stand-up", time.Date(2026, 8, 27, 8, 30, 0, 0, time.UTC), "stand-up"},
		{"tomorrow morning 8 stand-up", time.Date(2026, 8, 27, 8, 0, 0, 0, time.UTC), "stand-up"},

		// Absolute, with and without a clock.
		{"2026-09-01 14:30 ship it", time.Date(2026, 9, 1, 14, 30, 0, 0, time.UTC), "ship it"},
		{"2026-09-01 ship it", time.Date(2026, 9, 1, 9, 0, 0, 0, time.UTC), "ship it"},
	} {
		t.Run(tt.in, func(t *testing.T) {
			at, rest, err := Parse(tt.in, now)
			if err != nil {
				t.Fatalf("Parse(%q): %v", tt.in, err)
			}
			if !at.Equal(tt.wantAt) {
				t.Errorf("at = %s, want %s", at.Format(time.RFC3339), tt.wantAt.Format(time.RFC3339))
			}
			if rest != tt.want {
				t.Errorf("rest = %q, want %q", rest, tt.want)
			}
		})
	}
}

// Saying no clearly is half of what this is for. A time it cannot read
// has to be reported rather than guessed at, because the guess is a job
// that runs at a moment nobody chose.
func TestParseRefuses(t *testing.T) {
	for _, tt := range []struct{ in, because string }{
		{"", "nothing at all"},
		{"run the tests", "no time in it"},
		{"30분 뒤", "a time and nothing to do"},
		{"내일", "a day and nothing to do"},
		{"2020-01-01 09:00 do it", "a date in the past"},
		{"3 run the tests", "a bare number is not a clock"},
		{"오후 25시 deploy", "not a real hour"},
	} {
		t.Run(tt.because, func(t *testing.T) {
			if _, _, err := Parse(tt.in, now); err == nil {
				t.Errorf("Parse(%q) succeeded; it should refuse because it is %s", tt.in, tt.because)
			}
		})
	}
}

// The error a person actually reads has to show them the way out, so it
// carries examples rather than only a complaint.
func TestTheRefusalShowsExamples(t *testing.T) {
	_, _, err := Parse("run the tests", now)
	if err == nil {
		t.Fatal("expected a refusal")
	}
	if !strings.Contains(err.Error(), "30분 뒤") || !strings.Contains(err.Error(), "tomorrow 9am") {
		t.Errorf("the refusal does not show both languages: %v", err)
	}
}

// The echo is the whole reason parsing is allowed to guess: a wrong
// reading is caught here, before the work is booked.
func TestFormatSaysWhenInTermsAPersonChecks(t *testing.T) {
	for _, tt := range []struct {
		at   time.Time
		want string
	}{
		{now.Add(20 * time.Minute), "in 20 minutes"},
		{time.Date(2026, 8, 26, 18, 0, 0, 0, time.UTC), "today"},
		{time.Date(2026, 8, 27, 9, 0, 0, 0, time.UTC), "tomorrow"},
		{time.Date(2026, 9, 1, 9, 0, 0, 0, time.UTC), "2026-09-01"},
	} {
		if got := Format(tt.at, now); !strings.Contains(got, tt.want) {
			t.Errorf("Format(%s) = %q, want it to contain %q", tt.at.Format(time.RFC3339), got, tt.want)
		}
	}
}
