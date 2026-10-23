package automations

import (
	"math"
	"time"
)

// V8 Date emulation
//
// The scheduleRule math (schedule.go) ports computeScheduleRuleNextRunAt from
// packages/services/src/session/automationCron.ts, which builds *local* Dates
// field by field and compares instants. V8 defines `new Date(y, mo, d, h, mi,
// 0, 0)` in terms of local time with two DST rules that Go's time.Date does
// not share:
//
//   - gap (spring forward, 02:30 in a 02:00→03:00 jump): the wall time does
//     not exist; V8 interprets the fields in the offset in force *before* the
//     transition, which lands after it — the instant moves FORWARD by the gap
//     (02:30 → 03:30). time.Date resolves the same fields to the transition
//     instant instead.
//   - overlap (fall back, 01:30 twice): V8 picks the EARLIER instant, i.e.
//     the offset in force before the transition. time.Date picks the later.
//
// instantOf reproduces exactly those rules; every calendar candidate in this
// package (scheduleRule and cron alike) is mapped to an instant through it.
//
// A wall value is the proleptic-UTC epoch minute of a local calendar reading
// (year/month/day/hour/minute). Reading local fields off an instant (wallOf)
// and normalising overflowing fields (wallFields) are plain UTC arithmetic,
// exactly like V8's field normalisation (day 32 → next month, day 0 →
// previous month); DST never enters that step.
//
// zoneSegments enumerates the location's offsets around a reading, which is
// what lets instantOf decide "exists / ambiguous / skipped" without a table of
// transition rules.

// wall is a local calendar reading counted in epoch minutes.
type wall int64

// wallFields builds the reading y-mo-d h:mi, normalising overflow in UTC the
// way V8 normalises Date fields (mo may be outside 1..12, d outside 1..31).
func wallFields(year int, month time.Month, day, hour, minute int) wall {
	return wall(time.Date(year, month, day, hour, minute, 0, 0, time.UTC).Unix() / 60)
}

// wallOf reads the local calendar fields of instant in loc (getFullYear,
// getMonth, ...).
func wallOf(instant time.Time, loc *time.Location) wall {
	local := instant.In(loc)
	return wallFields(local.Year(), local.Month(), local.Day(), local.Hour(), local.Minute())
}

// wallOfDate is `new Date(y, m, d)` (midnight local reading).
func wallOfDate(year int, month time.Month, day int) wall {
	return wallFields(year, month, day, 0, 0)
}

// components splits a wall reading into local calendar fields.
func (w wall) components() (year int, month time.Month, day, hour, minute int) {
	t := time.Unix(int64(w)*60, 0).UTC()
	return t.Year(), t.Month(), t.Day(), t.Hour(), t.Minute()
}

// addDays is `d.setDate(d.getDate() + days)`: a calendar-day shift that keeps
// the clock reading (midnight stays midnight, across DST too).
func (w wall) addDays(days int) wall { return w + wall(days*minutesPerDay) }

// addMonths moves to the first of the month `months` later
// (`new Date(y, m + offset, 1)`); day 1 never overflows.
func (w wall) addMonths(months int) wall {
	year, month, _, _, _ := w.components()
	return wallOfDate(year, month+time.Month(months), 1)
}

// Accessors mirroring the JS Date getters used by the port.
func (w wall) dayOfMonth() int   { _, _, day, _, _ := w.components(); return day }
func (w wall) year() int         { year, _, _, _, _ := w.components(); return year }
func (w wall) month() time.Month { _, month, _, _, _ := w.components(); return month }
func (w wall) hour() int         { _, _, _, hour, _ := w.components(); return hour }

// at is `atTime(date, hour, minute)`: same date at h:mi.
func (w wall) at(hour, minute int) wall {
	year, month, day, _, _ := w.components()
	return wallFields(year, month, day, hour, minute)
}

// weekday is `d.getDay()` (Sunday = 0). 1970-01-01 was a Thursday (4).
func (w wall) weekday() int {
	days := int64(w) / minutesPerDay
	return int(((days+4)%7 + 7) % 7)
}

// intervalOf is Math.max(1, Math.floor(rule.interval)).
func intervalOf(value float64) int {
	if math.IsNaN(value) || math.IsInf(value, 0) {
		return 1
	}
	interval := int(math.Floor(value))
	if interval < 1 {
		return 1
	}
	return interval
}

// dateMode selects the reading of `new Date(fields)`:
//
//	dateV8      V8: gap → shift forward by the gap; overlap → earlier offset.
//	dateGoStyle time.Date: gap → transition instant; overlap → later offset.
//
// Production parity uses dateV8 (the fixtures come from Node). dateGoStyle
// exists so tests can pin the difference between the two.
type dateMode int

const (
	dateV8 dateMode = iota
	dateGoStyle
)

// offsetSegment is one constant-offset span of a location.
type offsetSegment struct {
	start  int64 // UTC seconds, inclusive
	end    int64 // UTC seconds, exclusive
	offset int64 // seconds east of UTC
}

func (s offsetSegment) contains(utc int64) bool { return utc >= s.start && utc < s.end }

// skipped reports whether the wall reading does not exist inside s: V8's
// pre-transition offset s.offset maps it to an instant outside s, and the
// post-transition offset maps it before s. For a spring-forward forward span
// (offset grows) these are exactly the gap's readings.
func (s offsetSegment) skipped(reading int64) bool {
	utc := reading - s.offset
	return utc < s.start || utc >= s.end
}

// zoneSegments enumerates the offset changes of loc within ±probeSlack of the
// wall reading (interpreted as UTC). Offsets are sampled every probeStep;
// each observed change is bisected between the last sample holding the old
// offset and the first sample holding the new one.
func zoneSegments(w wall, loc *time.Location) []offsetSegment {
	reading := int64(w) * 60
	from, to := reading-probeSlack, reading+probeSlack
	type sample struct{ at, offset int64 }
	var samples []sample
	for cursor := from; cursor <= to; cursor += probeStep {
		_, zoneOffset := time.Unix(cursor, 0).In(loc).Zone()
		samples = append(samples, sample{at: cursor, offset: int64(zoneOffset)})
	}
	segments := []offsetSegment{{start: from, end: from, offset: samples[0].offset}}
	for index := 1; index < len(samples); index++ {
		current := samples[index]
		last := &segments[len(segments)-1]
		last.end = current.at
		if current.offset == last.offset {
			continue
		}
		change := bisect(samples[index-1].at, current.at, last.offset, loc)
		last.end = change
		segments = append(segments, offsetSegment{start: change, end: current.at + probeStep, offset: current.offset})
	}
	return segments
}

// bisect returns the first second in (lo, hi] whose offset differs from want.
func bisect(lo, hi, want int64, loc *time.Location) int64 {
	for hi-lo > 1 {
		mid := lo + (hi-lo)/2
		if _, offset := time.Unix(mid, 0).In(loc).Zone(); int64(offset) == want {
			lo = mid
		} else {
			hi = mid
		}
	}
	return hi
}

// instantOf is `new Date(y, mo, d, h, mi, 0, 0)`: the instant V8 assigns to a
// local calendar reading (see the comment at the top of this file).
//
// A reading exists when some offset maps it into that offset's span; with two
// such offsets (fall-back overlap) V8 keeps the earlier instant (dateV8) or
// the later one (dateGoStyle). Otherwise the reading was skipped by a
// spring-forward: V8 reads its fields with the offset in force just before the
// transition, which lands after it.
func instantOf(w wall, loc *time.Location, mode dateMode) time.Time {
	reading := int64(w) * 60 // the reading as if it were UTC
	segments := zoneSegments(w, loc)
	var earliest, latest int64
	found := 0
	for _, segment := range segments {
		if utc := reading - segment.offset; segment.contains(utc) {
			if found == 0 || utc < earliest {
				earliest = utc
			}
			if found == 0 || utc > latest {
				latest = utc
			}
			found++
		}
	}
	switch {
	case found > 0 && mode == dateV8:
		return time.Unix(earliest, 0).UTC()
	case found > 0:
		return time.Unix(latest, 0).UTC()
	}
	// Skipped reading: find the forward transition whose old offset maps the
	// reading past that offset's end (the reading lies in the gap).
	for index := 1; index < len(segments); index++ {
		older, newer := segments[index-1], segments[index]
		if newer.offset <= older.offset {
			continue
		}
		if mapped := reading - older.offset; mapped >= older.start && mapped >= older.end {
			if mode == dateGoStyle {
				return time.Unix(newer.start, 0).UTC()
			}
			return time.Unix(mapped, 0).UTC()
		}
	}
	// Outside the probed span (never for a ±1 day DST jump): fall back to the
	// segment whose interval is nearest the reading.
	best := segments[0]
	for _, segment := range segments[1:] {
		if absInt64(reading-best.offset-best.start) > absInt64(reading-segment.offset-segment.start) {
			best = segment
		}
	}
	return time.Unix(reading-best.offset, 0).UTC()
}

func absInt64(value int64) int64 {
	if value < 0 {
		return -value
	}
	return value
}
