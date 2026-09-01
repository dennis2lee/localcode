package when

import (
	"testing"
	"time"
)

// The window asks for the moment and the request separately, so a field
// holding only a time is exactly right there and exactly wrong at a
// prompt. The two entry points differ in that one thing and share the
// rest, which is what keeps "내일 아침" meaning one moment whichever way
// it was typed.
func TestParseTimeAcceptsATimeWithNothingAfterIt(t *testing.T) {
	now := time.Date(2026, 8, 26, 16, 30, 0, 0, time.UTC)
	for _, tt := range []struct {
		in   string
		want time.Time
	}{
		{"30분 뒤", now.Add(30 * time.Minute)},
		{"내일 아침", time.Date(2026, 8, 27, 9, 0, 0, 0, time.UTC)},
		{"tomorrow 9am", time.Date(2026, 8, 27, 9, 0, 0, 0, time.UTC)},
		{"금요일 저녁", time.Date(2026, 8, 28, 18, 0, 0, 0, time.UTC)},
		{"2026-09-01 14:30", time.Date(2026, 9, 1, 14, 30, 0, 0, time.UTC)},
	} {
		at, _, err := ParseTime(tt.in, now)
		if err != nil {
			t.Errorf("ParseTime(%q): %v", tt.in, err)
			continue
		}
		if !at.Equal(tt.want) {
			t.Errorf("ParseTime(%q) = %s, want %s", tt.in, at.Format(time.RFC3339), tt.want.Format(time.RFC3339))
		}
	}
}

// And a field with the request typed into it as well is caught, rather
// than booking a moment and dropping the words after it.
func TestParseTimeRefusesAWholeSentence(t *testing.T) {
	now := time.Date(2026, 8, 26, 16, 30, 0, 0, time.UTC)
	for _, in := range []string{"30분 뒤 run the tests", "내일 아침에 테스트 돌려줘", "나중에"} {
		if _, _, err := ParseTime(in, now); err == nil {
			t.Errorf("ParseTime(%q) accepted a field that is not only a time", in)
		}
	}
}
