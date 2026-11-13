package scheduler

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// ErrInvalidSchedule is returned for a frequency that is not a valid
// five-field Cron expression.
var ErrInvalidSchedule = errors.New("scheduler: invalid cron expression")

// schedule is a parsed five-field Cron expression. Each field is a bit set of
// the values it matches.
type schedule struct {
	minute, hour, dom, month, dow uint64
	// domStar and dowStar record whether the field was "*" (or started with
	// "*/"): when both day fields are restricted a day matches if EITHER does.
	domStar, dowStar bool
	// fixed is true when minute and hour are both restricted ("30 1 * * *"):
	// a fixed-time job fires once per wall-clock occurrence, also on the day a
	// DST change repeats that hour. Otherwise ("*/15 * * * *", "0 * * * *")
	// it fires on every matching instant, so a repeated hour keeps firing.
	fixed bool
}

type fieldSpec struct {
	name     string
	min, max int
	names    map[string]int
}

var (
	minuteSpec = fieldSpec{name: "minute", min: 0, max: 59}
	hourSpec   = fieldSpec{name: "hour", min: 0, max: 23}
	domSpec    = fieldSpec{name: "day of month", min: 1, max: 31}
	monthSpec  = fieldSpec{name: "month", min: 1, max: 12, names: map[string]int{
		"jan": 1, "feb": 2, "mar": 3, "apr": 4, "may": 5, "jun": 6,
		"jul": 7, "aug": 8, "sep": 9, "oct": 10, "nov": 11, "dec": 12,
	}}
	// 7 is accepted as Sunday and folded into 0 after parsing.
	dowSpec = fieldSpec{name: "day of week", min: 0, max: 7, names: map[string]int{
		"sun": 0, "mon": 1, "tue": 2, "wed": 3, "thu": 4, "fri": 5, "sat": 6,
	}}
)

// parse reads a five-field expression: minute hour day-of-month month
// day-of-week. Each field accepts "*", a value, a range "a-b", a list
// "a,b,c", and a step "*/n" or "a-b/n". Month and weekday names (JAN, MON)
// are case-insensitive. Seconds, "@every" and the other descriptors are not
// part of the contract and are rejected.
func parse(expr string) (schedule, error) {
	fields := strings.Fields(expr)
	if len(fields) != 5 {
		return schedule{}, fmt.Errorf("%w %q: want 5 fields (minute hour day-of-month month day-of-week), got %d", ErrInvalidSchedule, expr, len(fields))
	}
	var s schedule
	var err error
	specs := []fieldSpec{minuteSpec, hourSpec, domSpec, monthSpec, dowSpec}
	dst := []*uint64{&s.minute, &s.hour, &s.dom, &s.month, &s.dow}
	for i, f := range fields {
		if *dst[i], err = parseField(f, specs[i]); err != nil {
			return schedule{}, fmt.Errorf("%w %q: %w", ErrInvalidSchedule, expr, err)
		}
	}
	s.fixed = !strings.HasPrefix(fields[0], "*") && !strings.HasPrefix(fields[1], "*")
	s.domStar = strings.HasPrefix(fields[2], "*")
	s.dowStar = strings.HasPrefix(fields[4], "*")
	if s.dow&(1<<7) != 0 { // 7 is Sunday
		s.dow = s.dow&^(1<<7) | 1
	}
	return s, nil
}

func parseField(field string, sp fieldSpec) (uint64, error) {
	var bits uint64
	for part := range strings.SplitSeq(field, ",") {
		b, err := parseRange(part, sp)
		if err != nil {
			return 0, fmt.Errorf("%s field %q: %w", sp.name, field, err)
		}
		bits |= b
	}
	return bits, nil
}

func parseRange(part string, sp fieldSpec) (uint64, error) {
	if part == "" {
		return 0, errors.New("empty list item")
	}
	rng, stepText, hasStep := strings.Cut(part, "/")
	step := 1
	if hasStep {
		n, err := strconv.Atoi(stepText)
		if err != nil || n < 1 || stepText != strconv.Itoa(n) {
			return 0, fmt.Errorf("step %q must be a positive integer", stepText)
		}
		step = n
	}
	var lo, hi int
	switch {
	case rng == "*":
		lo, hi = sp.min, sp.max
		if sp.name == "day of week" {
			hi = 6 // "*" must not include the alias 7
		}
	case strings.Contains(rng, "-"):
		a, b, _ := strings.Cut(rng, "-")
		var err error
		if lo, err = parseValue(a, sp); err != nil {
			return 0, err
		}
		if hi, err = parseValue(b, sp); err != nil {
			return 0, err
		}
		if lo > hi {
			return 0, fmt.Errorf("range %q is reversed", rng)
		}
	default:
		v, err := parseValue(rng, sp)
		if err != nil {
			return 0, err
		}
		if hasStep {
			// "5/15" is ambiguous between implementations; only "*/n" and "a-b/n" are accepted.
			return 0, fmt.Errorf("step %q needs \"*\" or a range, not a single value", part)
		}
		lo, hi = v, v
	}
	var bits uint64
	for v := lo; v <= hi; v += step {
		bits |= 1 << uint(v)
	}
	return bits, nil
}

func parseValue(s string, sp fieldSpec) (int, error) {
	if v, ok := sp.names[strings.ToLower(s)]; ok {
		return v, nil
	}
	n, err := strconv.Atoi(s)
	if err != nil || s != strconv.Itoa(n) {
		return 0, fmt.Errorf("%q is not a number or a known name", s)
	}
	if n < sp.min || n > sp.max {
		return 0, fmt.Errorf("%d is outside %d-%d", n, sp.min, sp.max)
	}
	return n, nil
}

// dayMatches applies the Vixie cron rule: when both day fields are restricted
// the day matches if either does; otherwise both must (a "*" always does).
func (s schedule) dayMatches(y int, m time.Month, d int) bool {
	domOK := s.dom&(1<<uint(d)) != 0
	dowOK := s.dow&(1<<uint(time.Date(y, m, d, 0, 0, 0, 0, time.UTC).Weekday())) != 0
	if !s.domStar && !s.dowStar {
		return domOK || dowOK
	}
	return domOK && dowOK
}

// maxSearchYears bounds the search for an expression that never fires, such as
// "0 0 31 2 *".
const maxSearchYears = 8

// nextWall returns the first wall-clock minute strictly after w that matches
// the schedule. The result is a civil time carried in UTC; the caller maps it
// to an instant in the schedule's location. ok is false when nothing matches
// within maxSearchYears.
func (s schedule) nextWall(w time.Time) (time.Time, bool) {
	t := time.Date(w.Year(), w.Month(), w.Day(), w.Hour(), w.Minute()+1, 0, 0, time.UTC)
	limit := t.Year() + maxSearchYears
	for t.Year() <= limit {
		switch {
		case s.month&(1<<uint(t.Month())) == 0:
			t = time.Date(t.Year(), t.Month()+1, 1, 0, 0, 0, 0, time.UTC)
		case !s.dayMatches(t.Year(), t.Month(), t.Day()):
			t = time.Date(t.Year(), t.Month(), t.Day()+1, 0, 0, 0, 0, time.UTC)
		case s.hour&(1<<uint(t.Hour())) == 0:
			t = time.Date(t.Year(), t.Month(), t.Day(), t.Hour()+1, 0, 0, 0, time.UTC)
		case s.minute&(1<<uint(t.Minute())) == 0:
			t = t.Add(time.Minute)
		default:
			return t, true
		}
	}
	return time.Time{}, false
}

// next returns the first instant strictly after t at which the schedule fires,
// evaluated on the wall clock of loc. DST (see the package documentation):
//   - A wall time that does not exist (spring forward) fires once, shifted by
//     the length of the gap (02:30 becomes 03:30), unless that instant was
//     already used.
//   - A wall time that occurs twice (fall back): a fixed-time job (minute and
//     hour both restricted) fires once, at its first occurrence; a job with a
//     wildcard minute or hour keeps firing on every matching instant of the
//     repeated hour.
func (s schedule) next(t time.Time, loc *time.Location) (time.Time, bool) {
	t = t.In(loc)
	w := time.Date(t.Year(), t.Month(), t.Day(), t.Hour(), t.Minute(), 0, 0, time.UTC)
	if !s.fixed {
		// Look back far enough to visit the wall times of a repeated hour a
		// second time; resolve keeps only instants after t.
		w = w.Add(-3 * time.Hour)
	}
	for range 4 * 366 * 24 * 60 { // a hard stop, never reached in practice
		c, ok := s.nextWall(w)
		if !ok {
			return time.Time{}, false
		}
		if inst, ok := resolve(c, loc, t); ok {
			return inst, true
		}
		w = c
	}
	return time.Time{}, false
}

// resolve maps the civil time c to the earliest instant after t whose wall
// clock in loc reads c; for a nonexistent wall time it returns the instant
// shifted by the gap. ok is false when the only candidates are not after t.
func resolve(c time.Time, loc *time.Location, t time.Time) (time.Time, bool) {
	// Candidate offsets: the ones in force a day before and a day after the
	// civil time bracket any single transition.
	var cands []time.Time
	seen := map[int]bool{}
	for _, probe := range []time.Duration{-24 * time.Hour, 0, 24 * time.Hour} {
		_, off := c.Add(probe).In(loc).Zone() // c is UTC-labelled: only a probe for the offset
		if seen[off] {
			continue
		}
		seen[off] = true
		inst := c.Add(-time.Duration(off) * time.Second)
		if w := inst.In(loc); w.Year() == c.Year() && w.Month() == c.Month() && w.Day() == c.Day() && w.Hour() == c.Hour() && w.Minute() == c.Minute() {
			cands = append(cands, inst)
		}
	}
	if len(cands) == 0 {
		// A gap: apply the offset in force before the transition, which moves
		// the wall time forward by the length of the gap (time.Date would pick
		// the offset after it and land before the gap instead).
		_, before := c.Add(-24 * time.Hour).In(loc).Zone()
		inst := c.Add(-time.Duration(before) * time.Second)
		return inst, inst.After(t)
	}
	best := time.Time{}
	for _, inst := range cands {
		if inst.After(t) && (best.IsZero() || inst.Before(best)) {
			best = inst
		}
	}
	return best, !best.IsZero()
}
