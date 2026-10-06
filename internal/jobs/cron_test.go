package jobs

import (
	"testing"
	"time"
)

func TestSchedule(t *testing.T) {
	base := time.Date(2026, 10, 6, 10, 7, 30, 0, time.UTC) // Tuesday
	cases := []struct{ expr, tz, want string }{
		{"*/15 * * * *", "", "2026-10-06T10:15:00Z"},
		{"0 2 * * *", "", "2026-10-07T02:00:00Z"},
		{"0 2 * * *", "Asia/Dhaka", "2026-10-06T20:00:00Z"}, // 02:00 +06
		{"30 9 * * mon-fri", "", "2026-10-07T09:30:00Z"},
		{"0 0 1 jan *", "", "2027-01-01T00:00:00Z"},
		{"@hourly", "", "2026-10-06T11:00:00Z"},
		{"0 12 * * 7", "", "2026-10-11T12:00:00Z"},   // Sunday as 7
		{"0 0 13 * fri", "", "2026-10-09T00:00:00Z"}, // dom OR dow
	}
	for _, c := range cases {
		s, err := ParseSchedule(c.expr, c.tz)
		if err != nil {
			t.Fatalf("%s: %v", c.expr, err)
		}
		if got := s.Next(base).UTC().Format(time.RFC3339); got != c.want {
			t.Errorf("%s (%s): got %s want %s", c.expr, c.tz, got, c.want)
		}
	}
	for _, bad := range []string{"", "* * * *", "60 * * * *", "* 24 * * *", "*/0 * * * *", "* * * foo *", "1-x * * * *"} {
		if _, err := ParseSchedule(bad, ""); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
	if _, err := ParseSchedule("* * * * *", "Mars/Olympus"); err == nil {
		t.Error("bad timezone accepted")
	}
}
