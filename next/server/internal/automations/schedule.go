package automations

import (
	"math"
	"sort"
	"strings"
	"time"
)

// V8 Date emulation
//
// The scheduleRule math ports computeScheduleRuleNextRunAt
// (packages/services/src/session/automationCron.ts), which builds *local*
// Dates field by field and compares instants. V8 defines `new Date(y, mo, d,
// h, mi, 0, 0)` in terms of local time with two DST rules that Go's
// time.Date does not share:
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
// file is produced through it, so the Go answers are the V8 ones.
//
// A wall value is the proleptic-UTC epoch minute of a local calendar reading
// (year/month/day/hour/minute). Reading local fields off an instant (wallOf)
// and normalising overflowing fields (wallFields) are plain UTC arithmetic,
// exactly like V8's field normalisation (day 32 → next month, day 0 →
// previous month); DST never enters that step.

const (
	minutesPerDay = 24 * 60

	// probeStep / probeSlack bound the zone scan around a wall reading: DST
	// offsets never exceed a day, so ±4 days is ample.
	probeStep  = 6 * 3600
	probeSlack = 4 * 24 * 3600
)

// isAfter reports a > b on the absolute instant.
func isAfter(a, b time.Time) bool { return since(a, b) > 0 }

// since returns a-b in nanoseconds from the absolute instant.
func since(a, b time.Time) int64 {
	return (a.Unix()-b.Unix())*1e9 + int64(a.Nanosecond()-b.Nanosecond())
}

// scheduleNext ports computeScheduleRuleNextRunAt literally: every branch
// builds candidates with V8 Date semantics and returns the first instant
// strictly after from.
func scheduleNext(rule *ScheduleRule, from time.Time, loc *time.Location) (time.Time, bool) {
	if rule == nil {
		return time.Time{}, false
	}
	fromMs := from.UnixMilli()
	interval := intervalOf(rule.Interval)
	anchorWall := wallOf(time.UnixMilli(int64(rule.AnchorAt)), loc)
	anchorMs := int64(rule.AnchorAt)

	switch rule.Unit {
	case UnitMinute:
		// Plain number arithmetic in the original (from, anchorAt are epoch
		// ms), so DST cannot influence the result and no Date is built.
		step := int64(interval) * 60 * 1000
		steps := (fromMs-anchorMs)/step + 1
		if steps < 1 {
			steps = 1
		}
		return time.UnixMilli(anchorMs + steps*step), true

	case UnitHourly:
		// base = new Date(anchor); base.setMinutes(rule.minute, 0, 0) is an
		// ordinary local-time setter: reading anchor's hour with the rule's
		// minute, resolved with V8 semantics; then step epoch milliseconds.
		base := instantOf(anchorWall.at(anchorWall.hour(), minuteOf(rule)), loc, dateV8).UnixMilli()
		step := int64(interval) * 60 * 60 * 1000
		steps := (fromMs-base)/step + 1
		if steps < 0 {
			steps = 0
		}
		return time.UnixMilli(base + steps*step), true

	case UnitDaily:
		for index := 0; index < 36600; index++ {
			date := anchorWall.addDays(index * interval)
			candidate := instantOf(date.at(hourOf(rule), minuteOf(rule)), loc, dateV8).UnixMilli()
			if candidate > fromMs {
				return time.UnixMilli(candidate), true
			}
		}
		return time.Time{}, false

	case UnitWeekly:
		// anchorWeek = midnight of the anchor date moved back to its Monday.
		anchorWeek := anchorWall.addDays(-((anchorWall.weekday() + 6) % 7))
		weekdays := rule.Weekdays
		if len(weekdays) == 0 {
			weekdays = []int{1}
		}
		ordered := append([]int(nil), weekdays...)
		sort.Ints(ordered)
		year, month, day := anchorWeek.year(), anchorWeek.month(), anchorWeek.dayOfMonth()
		for week := 0; week < 5220; week += interval {
			for _, weekday := range ordered {
				date := wallOfDate(year, month, day+week*7+(weekday+6)%7)
				candidate := instantOf(date.at(hourOf(rule), minuteOf(rule)), loc, dateV8).UnixMilli()
				if candidate > fromMs {
					return time.UnixMilli(candidate), true
				}
			}
		}
		return time.Time{}, false

	case UnitMonthly:
		for offset := 0; offset <= 1200; offset += interval {
			month := anchorWall.addMonths(offset)
			var candidates []wall
			if rule.MonthlyMode == "weekday" {
				target := 1
				if len(rule.Weekdays) > 0 {
					target = (rule.Weekdays[0]%7 + 7) % 7
				}
				// firstWeekdayOfMonth: day 1 + ((target - getDay(day1) + 7) % 7)
				candidates = []wall{wallOfDate(month.year(), month.month(), 1+((target-month.weekday())+7)%7)}
			} else {
				days := append([]int(nil), rule.MonthDays...)
				if len(days) == 0 {
					days = []int{1}
				}
				sort.Ints(days)
				for _, day := range days {
					date := wallOfDate(month.year(), month.month(), day)
					if date.month() != month.month() {
						continue // day 31 of a 30-day month rolls over: skipped
					}
					candidates = append(candidates, date)
				}
			}
			for _, date := range candidates {
				candidate := instantOf(date.at(hourOf(rule), minuteOf(rule)), loc, dateV8).UnixMilli()
				if candidate > fromMs {
					return time.UnixMilli(candidate), true
				}
			}
		}
		return time.Time{}, false

	case UnitYearly:
		targetMonth := anchorWall.month()
		if len(rule.Months) > 0 {
			targetMonth = time.Month((((rule.Months[0]-1)%12)+12)%12 + 1)
		}
		targetDay := anchorWall.dayOfMonth()
		if len(rule.MonthDays) > 0 {
			targetDay = rule.MonthDays[0]
		}
		for offset := 0; offset < 400; offset += interval {
			date := wallOfDate(anchorWall.year()+offset, targetMonth, targetDay)
			if date.month() != targetMonth {
				continue // Feb 30, or Feb 29 in a non-leap year
			}
			candidate := instantOf(date.at(hourOf(rule), minuteOf(rule)), loc, dateV8).UnixMilli()
			if candidate > fromMs {
				return time.UnixMilli(candidate), true
			}
		}
		return time.Time{}, false
	}
	return time.Time{}, false
}

// hourOf / minuteOf are `rule.hour` / `rule.minute` as integers.
func hourOf(rule *ScheduleRule) int   { return int(rule.Hour) }
func minuteOf(rule *ScheduleRule) int { return int(rule.Minute) }

// nextRunAt is the combined next-run computation: scheduleRule wins over
// cronExpr (computeAutomationNextRunAt).
func nextRunAt(a *Automation, from time.Time, loc *time.Location) (time.Time, bool) {
	if a.ScheduleRule != nil {
		return scheduleNext(a.ScheduleRule, from, loc)
	}
	expr, err := parseCronExpr(a.CronExpr)
	if err != nil {
		return time.Time{}, false
	}
	return expr.next(from, loc)
}

// isOneShot reports !recurring && maxRuns<=1 (isOneShotAutomation).
func isOneShot(a *Automation) bool {
	if a.Recurring {
		return false
	}
	if a.MaxRuns == nil {
		return true
	}
	return *a.MaxRuns <= 1
}

// inferMinuteIntervalScheduleRule mirrors inferMinuteIntervalScheduleRule.
func inferMinuteIntervalScheduleRule(cronExpr string, anchor time.Time, loc *time.Location) *ScheduleRule {
	fields := strings.Fields(cronExpr)
	if len(fields) != 5 {
		return nil
	}
	interval, ok := parseStep(fields[0], "*/")
	if !ok || interval < 1 {
		return nil
	}
	if fields[1] != "*" || fields[2] != "*" || fields[3] != "*" || fields[4] != "*" {
		return nil
	}
	local := anchor.In(loc)
	return &ScheduleRule{
		Unit:     UnitMinute,
		Interval: float64(interval),
		Hour:     float64(local.Hour()),
		Minute:   float64(local.Minute()),
		AnchorAt: float64(anchor.UnixMilli()),
	}
}

// parseStep parses a "<prefix><digits>" cron token (e.g. "*/10").
func parseStep(token, prefix string) (int, bool) {
	if !strings.HasPrefix(token, prefix) {
		return 0, false
	}
	value, ok := parseInt64(token[len(prefix):])
	if !ok || value > math.MaxInt32 {
		return 0, false
	}
	return int(value), true
}
