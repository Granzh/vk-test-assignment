// Package domain holds business entities and rules, free of I/O.
package domain

import "time"

// Calendar maps instants to campaign days.
//
// A campaign day is a calendar date in Location whose boundary is shifted by
// Boundary: with a boundary of 04:00, 03:59 on 2 September still belongs to
// the campaign day of 1 September.
//
// Dates are represented as time.Time at 00:00 UTC, so they compare with == and
// convert directly to a PostgreSQL DATE.
type Calendar struct {
	Location *time.Location
	// Boundary is the offset from local midnight at which a day starts.
	Boundary time.Duration
}

// DayOf returns the campaign day that the instant t falls into.
func (c Calendar) DayOf(t time.Time) time.Time {
	local := t.In(c.Location).Add(-c.Boundary)
	y, m, d := local.Date()
	return time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
}

// StartOf returns the instant at which the given campaign day begins.
func (c Calendar) StartOf(day time.Time) time.Time {
	y, m, d := day.Date()
	return time.Date(y, m, d, 0, 0, 0, 0, c.Location).Add(c.Boundary)
}
