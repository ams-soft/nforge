package scheduler

import (
	"errors"
	"testing"
	"time"
	_ "time/tzdata"
)

func mustLoc(t testing.TB, name string) *time.Location {
	t.Helper()
	loc, err := time.LoadLocation(name)
	if err != nil {
		t.Fatal(err)
	}
	return loc
}

func TestParseInvalid(t *testing.T) {
	for _, expr := range []string{
		"", "* * * *", "* * * * * *", "0 0 7 * * *", "@every 1m", "@daily",
		"60 * * * *", "* 24 * * *", "* * 0 * *", "* * 32 * *", "* * * 0 *", "* * * 13 *", "* * * * 8",
		"-1 * * * *", "a * * * *", "5-1 * * * *", "*/0 * * * *", "*/x * * * *", "5/15 * * * *",
		"1,,2 * * * *", ",1 * * * *", "1- * * * *", "01 * * * *", "* * * foo *", "*/-1 * * * *",
	} {
		if _, err := parse(expr); !errors.Is(err, ErrInvalidSchedule) {
			t.Errorf("parse(%q) = %v, want ErrInvalidSchedule", expr, err)
		}
	}
}

func TestParseValid(t *testing.T) {
	for _, expr := range []string{
		"0 7 * * *", "* * * * *", "*/15 * * * *", "0 9-17 * * 1-5", "0,30 * * * *", "0 0 1,15 * *",
		"0 0 * JAN-MAR MON", "0 0 * * sun", "0 0 * * 7", "10-50/10 * * * *", "  0   7  *  *  * ", "59 23 31 12 6",
	} {
		if _, err := parse(expr); err != nil {
			t.Errorf("parse(%q) = %v", expr, err)
		}
	}
}

func TestSundayAliases(t *testing.T) {
	a, _ := parse("0 0 * * 7")
	b, _ := parse("0 0 * * 0")
	if a.dow != b.dow || a.dow != 1 {
		t.Fatalf("dow = %b and %b, want both 1", a.dow, b.dow)
	}
	star, _ := parse("0 0 * * *")
	if star.dow != 0b1111111 {
		t.Fatalf("* in the weekday field = %b", star.dow)
	}
}

func at(loc *time.Location, y int, m time.Month, d, h, min int) time.Time {
	return time.Date(y, m, d, h, min, 0, 0, loc)
}

func nextOf(t *testing.T, expr string, from time.Time, loc *time.Location) time.Time {
	t.Helper()
	s, err := parse(expr)
	if err != nil {
		t.Fatal(err)
	}
	n, ok := s.next(from, loc)
	if !ok {
		t.Fatalf("%q never fires", expr)
	}
	return n
}

func TestNextDailyAtSeven(t *testing.T) {
	sp := mustLoc(t, "America/Sao_Paulo")
	// 07:00 in Sao Paulo (UTC-3, no DST since 2019) is 10:00 UTC.
	got := nextOf(t, "0 7 * * *", time.Date(2026, 11, 13, 9, 59, 0, 0, time.UTC), sp)
	if want := time.Date(2026, 11, 13, 10, 0, 0, 0, time.UTC); !got.Equal(want) {
		t.Fatalf("next = %v, want %v", got, want)
	}
	if got.In(sp).Hour() != 7 {
		t.Fatalf("wall clock = %v", got.In(sp))
	}
	// Exactly at 07:00 the next one is tomorrow: strictly after.
	got = nextOf(t, "0 7 * * *", got, sp)
	if want := time.Date(2026, 11, 14, 10, 0, 0, 0, time.UTC); !got.Equal(want) {
		t.Fatalf("next = %v, want %v", got, want)
	}
	// The same expression in UTC fires three hours earlier.
	got = nextOf(t, "0 7 * * *", time.Date(2026, 11, 13, 0, 0, 0, 0, time.UTC), time.UTC)
	if want := time.Date(2026, 11, 13, 7, 0, 0, 0, time.UTC); !got.Equal(want) {
		t.Fatalf("utc next = %v, want %v", got, want)
	}
}

func TestNextFields(t *testing.T) {
	utc := time.UTC
	for _, tc := range []struct {
		expr string
		from time.Time
		want time.Time
	}{
		{"*/15 * * * *", at(utc, 2026, 1, 1, 10, 7), at(utc, 2026, 1, 1, 10, 15)},
		{"0 9-17/4 * * *", at(utc, 2026, 1, 1, 10, 0), at(utc, 2026, 1, 1, 13, 0)},
		{"0 0 1 * *", at(utc, 2026, 1, 31, 12, 0), at(utc, 2026, 2, 1, 0, 0)},
		{"0 0 29 2 *", at(utc, 2026, 1, 1, 0, 0), at(utc, 2028, 2, 29, 0, 0)}, // next leap year
		{"0 0 * * 1-5", at(utc, 2026, 11, 13, 12, 0), at(utc, 2026, 11, 16, 0, 0)},
		{"0 0 * * 7", at(utc, 2026, 11, 13, 12, 0), at(utc, 2026, 11, 15, 0, 0)}, // Sunday
		// Both day fields restricted: either matches (13th OR Friday).
		{"0 0 13 * FRI", at(utc, 2026, 11, 1, 0, 0), at(utc, 2026, 11, 6, 0, 0)},
		{"0 0 13 * FRI", at(utc, 2026, 11, 7, 0, 0), at(utc, 2026, 11, 13, 0, 0)},
		// One of them "*": both must match.
		{"0 0 13 * *", at(utc, 2026, 11, 1, 0, 0), at(utc, 2026, 11, 13, 0, 0)},
		{"0 0 */2 JAN *", at(utc, 2026, 1, 2, 0, 0), at(utc, 2026, 1, 3, 0, 0)},
		{"59 23 31 12 *", at(utc, 2026, 1, 1, 0, 0), at(utc, 2026, 12, 31, 23, 59)},
	} {
		if got := nextOf(t, tc.expr, tc.from, utc); !got.Equal(tc.want) {
			t.Errorf("next(%q, %v) = %v, want %v", tc.expr, tc.from, got, tc.want)
		}
	}
}

func TestNeverFires(t *testing.T) {
	s, _ := parse("0 0 31 2 *")
	if n, ok := s.next(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), time.UTC); ok {
		t.Fatalf("Feb 31 fired at %v", n)
	}
}

func TestDSTSpringForward(t *testing.T) {
	ny := mustLoc(t, "America/New_York") // 2026-03-08 02:00 -> 03:00
	// 02:30 does not exist: it fires once, shifted to 03:30 EDT.
	got := nextOf(t, "30 2 * * *", at(ny, 2026, 3, 7, 12, 0), ny)
	if want := time.Date(2026, 3, 8, 7, 30, 0, 0, time.UTC); !got.Equal(want) {
		t.Fatalf("next = %v, want %v", got.UTC(), want)
	}
	// And the following day is back to 02:30.
	got = nextOf(t, "30 2 * * *", got, ny)
	if want := at(ny, 2026, 3, 9, 2, 30); !got.Equal(want) {
		t.Fatalf("next = %v, want %v", got, want)
	}
	// Hourly: 02:00 does not exist, 03:00 fires once, never twice.
	h := at(ny, 2026, 3, 8, 1, 0)
	var seq []time.Time
	for range 3 {
		h = nextOf(t, "0 * * * *", h, ny)
		seq = append(seq, h.UTC())
	}
	for i, w := range []time.Time{
		time.Date(2026, 3, 8, 7, 0, 0, 0, time.UTC), // 03:00 EDT
		time.Date(2026, 3, 8, 8, 0, 0, 0, time.UTC), // 04:00 EDT
		time.Date(2026, 3, 8, 9, 0, 0, 0, time.UTC),
	} {
		if !seq[i].Equal(w) {
			t.Errorf("hourly[%d] = %v, want %v", i, seq[i], w)
		}
	}
}

func TestDSTFallBack(t *testing.T) {
	ny := mustLoc(t, "America/New_York") // 2026-11-01 02:00 EDT -> 01:00 EST
	// A fixed-time job fires once, at the first 01:30 (EDT), then the next day.
	first := nextOf(t, "30 1 * * *", at(ny, 2026, 11, 1, 0, 0), ny)
	if want := time.Date(2026, 11, 1, 5, 30, 0, 0, time.UTC); !first.Equal(want) {
		t.Fatalf("first = %v, want %v", first.UTC(), want)
	}
	second := nextOf(t, "30 1 * * *", first, ny)
	if want := at(ny, 2026, 11, 2, 1, 30); !second.Equal(want) {
		t.Fatalf("second = %v, want the next day %v", second, want)
	}
	// Started during the second pass, the instant is still in the future: it is not lost.
	late := nextOf(t, "30 1 * * *", time.Date(2026, 11, 1, 6, 10, 0, 0, time.UTC), ny) // 01:10 EST
	if want := time.Date(2026, 11, 1, 6, 30, 0, 0, time.UTC); !late.Equal(want) {
		t.Fatalf("late = %v, want %v", late.UTC(), want)
	}
	// A wildcard-hour job keeps firing through the repeated hour.
	h := at(ny, 2026, 11, 1, 0, 30)
	var seq []time.Time
	for range 4 {
		h = nextOf(t, "0 * * * *", h, ny)
		seq = append(seq, h.UTC())
	}
	for i, w := range []time.Time{
		time.Date(2026, 11, 1, 5, 0, 0, 0, time.UTC), // 01:00 EDT
		time.Date(2026, 11, 1, 6, 0, 0, 0, time.UTC), // 01:00 EST
		time.Date(2026, 11, 1, 7, 0, 0, 0, time.UTC), // 02:00 EST
		time.Date(2026, 11, 1, 8, 0, 0, 0, time.UTC),
	} {
		if !seq[i].Equal(w) {
			t.Errorf("hourly[%d] = %v, want %v", i, seq[i], w)
		}
	}
}

// Every result is strictly after the start and the sequence is increasing,
// across a whole year of DST changes, for several zones and shapes.
func TestNextIsMonotonic(t *testing.T) {
	for _, zone := range []string{"UTC", "America/New_York", "Europe/London", "Australia/Lord_Howe", "America/Sao_Paulo"} {
		loc := mustLoc(t, zone)
		for _, expr := range []string{"*/20 * * * *", "30 2 * * *", "0 * * * *", "15 1 * * *", "0 0 * * *"} {
			s, _ := parse(expr)
			cur := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
			for range 800 {
				n, ok := s.next(cur, loc)
				if !ok || !n.After(cur) {
					t.Fatalf("%s %q: next(%v) = %v, %v", zone, expr, cur, n, ok)
				}
				cur = n
			}
		}
	}
}
