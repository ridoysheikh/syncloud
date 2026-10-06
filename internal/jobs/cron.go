package jobs

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Schedule is a parsed 5-field cron expression (minute hour day-of-month
// month day-of-week) with *, lists, ranges, steps and @hourly/@daily/…
type Schedule struct {
	minute, hour, dom, month, dow uint64 // bit sets
	domStar, dowStar              bool
	loc                           *time.Location
}

var macros = map[string]string{
	"@yearly": "0 0 1 1 *", "@annually": "0 0 1 1 *", "@monthly": "0 0 1 * *",
	"@weekly": "0 0 * * 0", "@daily": "0 0 * * *", "@midnight": "0 0 * * *", "@hourly": "0 * * * *",
}

var monthNames = map[string]int{"jan": 1, "feb": 2, "mar": 3, "apr": 4, "may": 5, "jun": 6, "jul": 7, "aug": 8, "sep": 9, "oct": 10, "nov": 11, "dec": 12}
var dowNames = map[string]int{"sun": 0, "mon": 1, "tue": 2, "wed": 3, "thu": 4, "fri": 5, "sat": 6}

// ParseSchedule parses expr in timezone tz ("" = UTC).
func ParseSchedule(expr, tz string) (*Schedule, error) {
	loc := time.UTC
	if tz != "" {
		l, err := time.LoadLocation(tz)
		if err != nil {
			return nil, fmt.Errorf("unknown timezone %q", tz)
		}
		loc = l
	}
	expr = strings.TrimSpace(expr)
	if m, ok := macros[strings.ToLower(expr)]; ok {
		expr = m
	}
	f := strings.Fields(expr)
	if len(f) != 5 {
		return nil, fmt.Errorf("schedule %q: want 5 fields (minute hour day month weekday)", expr)
	}
	s := &Schedule{loc: loc, domStar: f[2] == "*", dowStar: f[4] == "*"}
	var err error
	if s.minute, err = field(f[0], 0, 59, nil); err != nil {
		return nil, fmt.Errorf("minute: %w", err)
	}
	if s.hour, err = field(f[1], 0, 23, nil); err != nil {
		return nil, fmt.Errorf("hour: %w", err)
	}
	if s.dom, err = field(f[2], 1, 31, nil); err != nil {
		return nil, fmt.Errorf("day of month: %w", err)
	}
	if s.month, err = field(f[3], 1, 12, monthNames); err != nil {
		return nil, fmt.Errorf("month: %w", err)
	}
	if s.dow, err = field(f[4], 0, 7, dowNames); err != nil {
		return nil, fmt.Errorf("day of week: %w", err)
	}
	if s.dow&(1<<7) != 0 { // 7 = Sunday
		s.dow |= 1
	}
	return s, nil
}

func field(f string, lo, hi int, names map[string]int) (uint64, error) {
	var bits uint64
	for _, part := range strings.Split(f, ",") {
		step := 1
		if base, st, ok := strings.Cut(part, "/"); ok {
			n, err := strconv.Atoi(st)
			if err != nil || n < 1 {
				return 0, fmt.Errorf("bad step %q", st)
			}
			part, step = base, n
		}
		start, end := lo, hi
		if part != "*" {
			a, b, isRange := strings.Cut(part, "-")
			var err error
			if start, err = value(a, names); err != nil {
				return 0, err
			}
			end = start
			if isRange {
				if end, err = value(b, names); err != nil {
					return 0, err
				}
			} else if step > 1 {
				end = hi // "5/15" means from 5 every 15
			}
		}
		if start < lo || end > hi || start > end {
			return 0, fmt.Errorf("%q out of range %d–%d", part, lo, hi)
		}
		for v := start; v <= end; v += step {
			bits |= 1 << v
		}
	}
	return bits, nil
}

func value(s string, names map[string]int) (int, error) {
	if v, ok := names[strings.ToLower(s)]; ok {
		return v, nil
	}
	v, err := strconv.Atoi(s)
	if err != nil {
		return 0, fmt.Errorf("bad value %q", s)
	}
	return v, nil
}

func (s *Schedule) dayMatches(t time.Time) bool {
	dom := s.dom&(1<<t.Day()) != 0
	dow := s.dow&(1<<int(t.Weekday())) != 0
	// Like cron: when both are restricted, either may match.
	switch {
	case s.domStar && s.dowStar:
		return true
	case s.domStar:
		return dow
	case s.dowStar:
		return dom
	}
	return dom || dow
}

// Next returns the first matching time strictly after t.
func (s *Schedule) Next(t time.Time) time.Time {
	t = t.In(s.loc).Truncate(time.Minute).Add(time.Minute)
	limit := t.AddDate(5, 0, 0)
	for t.Before(limit) {
		switch {
		case s.month&(1<<int(t.Month())) == 0:
			t = time.Date(t.Year(), t.Month()+1, 1, 0, 0, 0, 0, s.loc)
		case !s.dayMatches(t):
			t = time.Date(t.Year(), t.Month(), t.Day()+1, 0, 0, 0, 0, s.loc)
		case s.hour&(1<<t.Hour()) == 0:
			t = time.Date(t.Year(), t.Month(), t.Day(), t.Hour()+1, 0, 0, 0, s.loc)
		case s.minute&(1<<t.Minute()) == 0:
			t = t.Add(time.Minute)
		default:
			return t
		}
	}
	return time.Time{}
}
