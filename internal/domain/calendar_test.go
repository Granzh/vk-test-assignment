package domain

import (
	"testing"
	"time"
)

func mustLoc(t *testing.T, name string) *time.Location {
	t.Helper()
	loc, err := time.LoadLocation(name)
	if err != nil {
		t.Fatal(err)
	}
	return loc
}

func date(y int, m time.Month, d int) time.Time {
	return time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
}

func TestDayOfMidnightBoundary(t *testing.T) {
	msk := mustLoc(t, "Europe/Moscow")
	cal := Calendar{Location: msk}

	tests := []struct {
		name string
		at   time.Time
		want time.Time
	}{
		{"just before midnight", time.Date(2026, 9, 1, 23, 59, 59, 999_000_000, msk), date(2026, 9, 1)},
		{"exactly midnight", time.Date(2026, 9, 2, 0, 0, 0, 0, msk), date(2026, 9, 2)},
		{"just after midnight", time.Date(2026, 9, 2, 0, 0, 1, 0, msk), date(2026, 9, 2)},
		// 21:00 UTC is 00:00 in Moscow: the UTC date is still 1 September.
		{"utc instant across the boundary", time.Date(2026, 9, 1, 21, 0, 0, 0, time.UTC), date(2026, 9, 2)},
		{"utc instant just before", time.Date(2026, 9, 1, 20, 59, 59, 0, time.UTC), date(2026, 9, 1)},
		{"month change", time.Date(2026, 10, 1, 0, 0, 0, 0, msk), date(2026, 10, 1)},
		{"year change", time.Date(2027, 1, 1, 0, 0, 0, 0, msk), date(2027, 1, 1)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := cal.DayOf(tt.at); !got.Equal(tt.want) {
				t.Errorf("DayOf(%v) = %v, want %v", tt.at, got, tt.want)
			}
		})
	}
}

func TestDayOfCustomBoundary(t *testing.T) {
	msk := mustLoc(t, "Europe/Moscow")
	cal := Calendar{Location: msk, Boundary: 4 * time.Hour}

	tests := []struct {
		name string
		at   time.Time
		want time.Time
	}{
		{"after midnight still previous day", time.Date(2026, 9, 2, 3, 59, 59, 0, msk), date(2026, 9, 1)},
		{"at the boundary", time.Date(2026, 9, 2, 4, 0, 0, 0, msk), date(2026, 9, 2)},
		{"evening", time.Date(2026, 9, 2, 23, 0, 0, 0, msk), date(2026, 9, 2)},
		{"first night of the year", time.Date(2027, 1, 1, 2, 0, 0, 0, msk), date(2026, 12, 31)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := cal.DayOf(tt.at); !got.Equal(tt.want) {
				t.Errorf("DayOf(%v) = %v, want %v", tt.at, got, tt.want)
			}
		})
	}
}

func TestDayOfOtherZone(t *testing.T) {
	// UTC+12: the local date is ahead of UTC for most of the day.
	cal := Calendar{Location: mustLoc(t, "Asia/Kamchatka")}
	at := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC) // 00:00 on 2 September locally
	if got, want := cal.DayOf(at), date(2026, 9, 2); !got.Equal(want) {
		t.Errorf("DayOf = %v, want %v", got, want)
	}
}

func TestDayOfSteps(t *testing.T) {
	msk := mustLoc(t, "Europe/Moscow")
	cal := Calendar{Location: msk, Boundary: 90 * time.Minute}
	start := time.Date(2026, 9, 1, 1, 30, 0, 0, msk)
	prev := cal.DayOf(start)
	for i := 1; i <= 72; i++ {
		cur := cal.DayOf(start.Add(time.Duration(i) * time.Hour))
		if d := cur.Sub(prev); d != 0 && d != 24*time.Hour {
			t.Fatalf("step %d: day jumped by %v", i, d)
		}
		prev = cur
	}
	if want := date(2026, 9, 4); !prev.Equal(want) {
		t.Errorf("after 72h day = %v, want %v", prev, want)
	}
}

func TestStartOf(t *testing.T) {
	msk := mustLoc(t, "Europe/Moscow")
	cal := Calendar{Location: msk, Boundary: 4 * time.Hour}
	start := cal.StartOf(date(2026, 9, 2))
	if want := time.Date(2026, 9, 2, 4, 0, 0, 0, msk); !start.Equal(want) {
		t.Fatalf("StartOf = %v, want %v", start, want)
	}
	if got := cal.DayOf(start); !got.Equal(date(2026, 9, 2)) {
		t.Errorf("DayOf(StartOf) = %v", got)
	}
	if got := cal.DayOf(start.Add(-time.Nanosecond)); !got.Equal(date(2026, 9, 1)) {
		t.Errorf("DayOf(StartOf-1ns) = %v", got)
	}
}
