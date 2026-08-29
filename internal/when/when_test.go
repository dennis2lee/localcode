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

		// The particle belongs to the time, not to the request. Before
		// this, the most natural Korean phrasing booked the right moment
		// and handed the model "에 테스트 돌려줘".
		{"내일 아침에 테스트 돌려줘", time.Date(2026, 8, 27, 9, 0, 0, 0, time.UTC), "테스트 돌려줘"},
		{"3시간 후에 빌드 확인", now.Add(3 * time.Hour), "빌드 확인"},
		{"30분 뒤에 리포트 작성", now.Add(30 * time.Minute), "리포트 작성"},
		{"5시까지 끝내줘", time.Date(2026, 8, 26, 17, 0, 0, 0, time.UTC), "끝내줘"},
		// ... and a request that merely starts with the same letters is
		// not a particle. "에러" is not "에".
		{"내일 아침 에러 로그 확인", time.Date(2026, 8, 27, 9, 0, 0, 0, time.UTC), "에러 로그 확인"},

		// A bare hour means the nearest one. Said at half past four,
		// "5시" is the five half an hour away, not the one nineteen hours
		// away that happens to be a morning.
		{"5시 deploy", time.Date(2026, 8, 26, 17, 0, 0, 0, time.UTC), "deploy"},
		{"9시에 리포트", time.Date(2026, 8, 26, 21, 0, 0, 0, time.UTC), "리포트"},
		// Unless a day was named, or the hour was written unambiguously.
		{"내일 9시에 리포트", time.Date(2026, 8, 27, 9, 0, 0, 0, time.UTC), "리포트"},
		{"오전 9시 리포트", time.Date(2026, 8, 27, 9, 0, 0, 0, time.UTC), "리포트"},
		{"18:00 deploy", time.Date(2026, 8, 26, 18, 0, 0, 0, time.UTC), "deploy"},

		// tonight was in the day list and the time list both, so the day
		// list ate it and threw the hour away: it came out as nine in the
		// morning, already past, refused as "in the past".
		{"tonight write the notes", time.Date(2026, 8, 26, 21, 0, 0, 0, time.UTC), "write the notes"},
		// "at" belongs to the time, and was keeping the named forms from
		// ever being reached.
		{"at noon summarize the diff", time.Date(2026, 8, 27, 12, 0, 0, 0, time.UTC), "summarize the diff"},

		// Weekdays. The next one, strictly ahead of us.
		{"금요일 저녁에 배포", time.Date(2026, 8, 28, 18, 0, 0, 0, time.UTC), "배포"},
		{"friday evening deploy", time.Date(2026, 8, 28, 18, 0, 0, 0, time.UTC), "deploy"},
		{"수요일 리뷰", time.Date(2026, 9, 2, 9, 0, 0, 0, time.UTC), "리뷰"},
		{"다음주 월요일에 리뷰", time.Date(2026, 9, 7, 9, 0, 0, 0, time.UTC), "리뷰"},
		{"next monday review this", time.Date(2026, 9, 7, 9, 0, 0, 0, time.UTC), "review this"},

		// The counts people write out instead of typing a digit.
		{"in half an hour check the build", now.Add(30 * time.Minute), "check the build"},
		{"in a couple of hours ping me", now.Add(2 * time.Hour), "ping me"},
		{"한 시간 뒤에 확인", now.Add(time.Hour), "확인"},

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
		// Vague, and refused by name rather than by "could not read a
		// time": nobody has chosen a moment, and a scheduler must not
		// choose one for them.
		{"나중에 정리해줘", "nobody has picked a time"},
		{"곧 확인해줘", "the same, one word shorter"},
		{"later today check it", "the English one"},
		// A repeat asked for and silently turned into a single job at the
		// wrong time is the worst of the three outcomes. "1시간마다" used
		// to book one run at one in the morning with "간마다 확인" as the
		// request.
		{"매일 아침 9시에 리포트", "a repeat"},
		{"1시간마다 확인", "a repeat, half-parsed"},
		{"every day at 9am report", "the English repeat"},
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

// A refusal has to say which kind of no it is. "Could not read a time"
// sends somebody looking for a spelling mistake when the real answer is
// that repeats do not exist, or that they have not chosen a moment yet.
func TestTheRefusalSaysWhichKindOfNo(t *testing.T) {
	for _, tt := range []struct{ in, want string }{
		{"매일 9시에 리포트", "repeating"},
		{"1시간마다 확인", "repeating"},
		{"나중에 정리해줘", "is not a time"},
		{"later check it", "is not a time"},
	} {
		_, _, err := Parse(tt.in, now)
		if err == nil {
			t.Fatalf("Parse(%q) succeeded", tt.in)
		}
		if !strings.Contains(err.Error(), tt.want) {
			t.Errorf("Parse(%q) said %q, want it to mention %q", tt.in, err, tt.want)
		}
	}
}

// A word that merely starts like a particle or a repeat is neither.
func TestOrdinaryWordsAreNotSwallowed(t *testing.T) {
	for _, tt := range []struct{ in, wantPrompt string }{
		{"내일 아침 에러 로그 확인", "에러 로그 확인"},
		{"30분 뒤 everything in the queue", "everything in the queue"},
		{"내일 아침 경로 정리", "경로 정리"},
	} {
		_, rest, err := Parse(tt.in, now)
		if err != nil {
			t.Fatalf("Parse(%q): %v", tt.in, err)
		}
		if rest != tt.wantPrompt {
			t.Errorf("Parse(%q) prompt = %q, want %q", tt.in, rest, tt.wantPrompt)
		}
	}
}
