package when

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

// TestKoreanCompoundParticlesAreStrippedCleanly verifies that compound particles
// like "에다가" and "에다" are completely stripped after a clock expression without
// leaving dangling fragments ("다가", "다") in the prompt, and that ordinary words
// opening with "에" are never mangled by the clock parser or particle stripper.
func TestKoreanCompoundParticlesAreStrippedCleanly(t *testing.T) {
	testNow := time.Date(2026, 8, 26, 14, 0, 0, 0, time.UTC)
	for _, tt := range []struct {
		in         string
		wantPrompt string
	}{
		// Compound particles after a clock time must be stripped completely.
		{"3시에다가 확인", "확인"},
		{"3시에다 확인", "확인"},
		{"3시에 확인", "확인"},
		{"3시 30분에다가 배포", "배포"},
		{"3시 30분에다 배포", "배포"},
		{"3시 30분에 배포", "배포"},

		// Ordinary Korean words starting with "에" following a bare clock
		// must survive untouched: the clock parser must not eat "에" via
		// a greedy or boundary-less match.
		{"3시 에러 로그 확인", "에러 로그 확인"},
		{"3시 에이전트 실행", "에이전트 실행"},
		{"3시 에너지 점검", "에너지 점검"},
		{"3시 에피소드 요약", "에피소드 요약"},
		{"3시 에디터 열기", "에디터 열기"},
		{"3시 엔드포인트 점검", "엔드포인트 점검"},

		// When a particle IS present before a word opening with "에",
		// only the particle is stripped, preserving the request word.
		{"3시에 에러 로그 확인", "에러 로그 확인"},
		{"3시에다가 에러 로그 확인", "에러 로그 확인"},
		{"3시에다 에러 로그 확인", "에러 로그 확인"},
	} {
		_, _, rest, err := Parse(tt.in, testNow)
		if err != nil {
			t.Fatalf("Parse(%q): %v", tt.in, err)
		}
		if rest != tt.wantPrompt {
			t.Errorf("Parse(%q) rest = %q, want %q", tt.in, rest, tt.wantPrompt)
		}
	}
}

// TestEveryNaturalLanguageDateVocabularyEntryParses walks all eight vocabulary
// tables in internal/when and asserts each entry can be reached by the real parser.
func TestEveryNaturalLanguageDateVocabularyEntryParses(t *testing.T) {
	testNow := time.Date(2026, 8, 26, 14, 0, 0, 0, time.UTC)

	t.Run("wordNumbers", func(t *testing.T) {
		for _, w := range wordNumbers {
			in := w.phrase + " do the task"
			at, _, rest, err := Parse(in, testNow)
			if err != nil {
				t.Errorf("wordNumbers %q failed: %v", w.phrase, err)
				continue
			}
			expectedAt := testNow.Add(w.d).Truncate(time.Second)
			if !at.Equal(expectedAt) {
				t.Errorf("wordNumbers %q parsed as %v, want %v", w.phrase, at, expectedAt)
			}
			if rest != "do the task" {
				t.Errorf("wordNumbers %q leftover rest = %q, want %q", w.phrase, rest, "do the task")
			}
		}
	})

	t.Run("dayWordsWithClock", func(t *testing.T) {
		for _, d := range dayWords {
			// Paired with a clock time in the future, every dayWord must advance
			// the calendar by d.days.
			in := d.word + " 18:00 do the task"
			at, _, rest, err := Parse(in, testNow)
			if err != nil {
				t.Errorf("dayWords %q failed: %v", d.word, err)
				continue
			}
			expectedDate := testNow.AddDate(0, 0, d.days)
			if at.Year() != expectedDate.Year() || at.Month() != expectedDate.Month() || at.Day() != expectedDate.Day() || at.Hour() != 18 {
				t.Errorf("dayWords %q parsed as %v, want date %v 18:00", d.word, at, expectedDate)
			}
			if rest != "do the task" {
				t.Errorf("dayWords %q leftover rest = %q, want %q", d.word, rest, "do the task")
			}
		}
	})

	t.Run("dayWordsBareMorning", func(t *testing.T) {
		// Bare day words without a clock time default to 9:00 AM. When evaluated
		// in the morning (before 9:00 AM), all entries including "오늘" and "today"
		// must successfully schedule for 9:00 AM on the corresponding day.
		morningNow := time.Date(2026, 8, 26, 8, 0, 0, 0, time.UTC)
		for _, d := range dayWords {
			in := d.word + " do the task"
			at, _, rest, err := Parse(in, morningNow)
			if err != nil {
				t.Errorf("dayWords bare %q at 08:00 failed: %v", d.word, err)
				continue
			}
			expectedDate := morningNow.AddDate(0, 0, d.days)
			if at.Year() != expectedDate.Year() || at.Month() != expectedDate.Month() || at.Day() != expectedDate.Day() || at.Hour() != 9 {
				t.Errorf("dayWords bare %q parsed as %v, want date %v 09:00", d.word, at, expectedDate)
			}
			if rest != "do the task" {
				t.Errorf("dayWords bare %q leftover rest = %q, want %q", d.word, rest, "do the task")
			}
		}
	})

	t.Run("dayWordsTodayAfternoonRefused", func(t *testing.T) {
		// When evaluated in the afternoon, bare "오늘" and "today" (without a clock)
		// must fail because 9:00 AM today is already in the past.
		afternoonNow := time.Date(2026, 8, 26, 14, 0, 0, 0, time.UTC)
		for _, w := range []string{"오늘", "today"} {
			in := w + " do the task"
			_, _, _, err := Parse(in, afternoonNow)
			if err == nil {
				t.Errorf("bare %q in afternoon unexpectedly succeeded", w)
			} else if !strings.Contains(err.Error(), "is in the past") {
				t.Errorf("bare %q in afternoon err = %v, want mention of 'is in the past'", w, err)
			}
		}
	})

	t.Run("namedTimes", func(t *testing.T) {
		for _, n := range namedTimes {
			in := n.word + " do the task"
			at, _, rest, err := Parse(in, testNow)
			if err != nil {
				t.Errorf("namedTimes %q failed: %v", n.word, err)
				continue
			}
			if at.Hour() != n.hour {
				t.Errorf("namedTimes %q hour = %d, want %d", n.word, at.Hour(), n.hour)
			}
			if rest != "do the task" {
				t.Errorf("namedTimes %q leftover rest = %q, want %q", n.word, rest, "do the task")
			}
		}
	})

	t.Run("weekdays", func(t *testing.T) {
		for _, w := range weekdays {
			in := w.word + " do the task"
			at, _, rest, err := Parse(in, testNow)
			if err != nil {
				t.Errorf("weekdays %q failed: %v", w.word, err)
				continue
			}
			if at.Weekday() != w.day {
				t.Errorf("weekdays %q weekday = %v, want %v", w.word, at.Weekday(), w.day)
			}
			if rest != "do the task" {
				t.Errorf("weekdays %q leftover rest = %q, want %q", w.word, rest, "do the task")
			}
		}
	})

	t.Run("vagueWords", func(t *testing.T) {
		for _, v := range vagueWords {
			in := v + " do the task"
			_, _, _, err := Parse(in, testNow)
			if err == nil {
				t.Errorf("vagueWords %q unexpectedly parsed without error", v)
			} else if !strings.Contains(err.Error(), fmt.Sprintf("%q is not a time", v)) {
				t.Errorf("vagueWords %q err = %v, want it to mention %q is not a time", v, err, v)
			}
		}
	})

	t.Run("particles", func(t *testing.T) {
		for _, p := range particles {
			in := "3시" + p + " 확인"
			at, _, rest, err := Parse(in, testNow)
			if err != nil {
				t.Errorf("particles %q failed: %v", p, err)
				continue
			}
			if at.Hour() != 15 {
				t.Errorf("particles %q hour = %d, want 15", p, at.Hour())
			}
			if rest != "확인" {
				t.Errorf("particles %q leftover rest = %q, want %q", p, rest, "확인")
			}
		}
	})

	t.Run("repeatPrefixes", func(t *testing.T) {
		for _, rp := range repeatPrefixes {
			in := rp.word + " do the task"
			_, rep, rest, err := Parse(in, testNow)
			if err != nil {
				t.Errorf("repeatPrefixes %q failed: %v", rp.word, err)
				continue
			}
			if rep != rp.rule {
				t.Errorf("repeatPrefixes %q rep = %v, want %v", rp.word, rep, rp.rule)
			}
			if rest != "do the task" {
				t.Errorf("repeatPrefixes %q leftover rest = %q, want %q", rp.word, rest, "do the task")
			}
		}
	})

	t.Run("monthWords", func(t *testing.T) {
		for _, m := range monthWords {
			in := m + " do the task"
			_, _, _, err := Parse(in, testNow)
			if err == nil {
				t.Errorf("monthWords %q unexpectedly parsed without error", m)
			} else if !strings.Contains(err.Error(), "repeats by the month") {
				t.Errorf("monthWords %q err = %v, want month refusal", m, err)
			}
		}
	})
}
