package automations

import (
	"fmt"
	"strings"
	"time"

	"github.com/robfig/cron/v3"
)

// cronParser accepts exactly the 5-field form the product documents ("5 段
// cron"). Ranges, steps, lists and names are allowed; @descriptors, seconds
// and years are rejected because the old client parsed 5 fields and the
// editor only ever sends 5 fields. robfig validates the expression; matching
// is done here on wall-clock fields (croner semantics), because robfig
// compares instants and silently mis-handles DST transitions.
var cronParser = cron.NewParser(cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow)

// cronExpr is a parsed 5-field expression plus the field sets used to match a
// wall-clock minute.
type cronExpr struct {
	text string

	minutes  [60]bool
	hours    [24]bool
	days     [32]bool // index 1..31
	months   [13]bool // index 1..12
	weekdays [7]bool  // 0 = Sunday
	domStar  bool
	dowStar  bool
}

// parseCronExpr validates and indexes a 5-field cron expression.
func parseCronExpr(expr string) (cronExpr, error) {
	trimmed := strings.TrimSpace(expr)
	if trimmed == "" {
		return cronExpr{}, fmt.Errorf("empty cron expression")
	}
	fields := strings.Fields(trimmed)
	if len(fields) != 5 {
		return cronExpr{}, fmt.Errorf("cron expression needs 5 fields, got %d", len(fields))
	}
	if _, err := cronParser.Parse(trimmed); err != nil {
		return cronExpr{}, err
	}
	parsed := cronExpr{
		text:    trimmed,
		domStar: fields[2] == "*" || fields[2] == "?",
		dowStar: fields[4] == "*" || fields[4] == "?",
	}
	if err := parsed.parseFields(fields); err != nil {
		return cronExpr{}, err
	}
	return parsed, nil
}

// horizonDays bounds a search: the old implementation used two years for a
// fixed calendar cron; four years covers every leap-day pattern.
const horizonDays = 366 * 4

// next returns the first match strictly after from, in loc.
func (c cronExpr) next(from time.Time, loc *time.Location) (time.Time, bool) {
	return c.nextMatch(from, loc)
}

// previous returns the latest match at or before from.
func (c cronExpr) previous(from time.Time, loc *time.Location) (time.Time, bool) {
	return c.previousMatch(from, loc)
}

// The search below reproduces computeNextRunAt = `new Cron(expr).nextRun(from)`
// (croner 10.0.1). croner walks CronDate instants one second at a time and
// raises the smallest non-matching field; for a 5-field pattern only second 0
// can match and every correction moves forward, so the walk visits the local
// readings of a day in order, each at most once. The Go model is therefore a
// single ordered pass over readings, with two DST rules verified against
// croner 10.0.1 (US and EU, 2026):
//
//   - fall back (repeated hour): the second pass's readings are never visited
//     (01:59 EDT + 1 s reads 01:00 EST, a minute that already passed), so an
//     ambiguous reading fires ONCE, at its earlier instant;
//   - spring forward (skipped readings): 01:59 EST + 1 s reads 03:00 and the
//     walk continues 03:00, 03:01, …, so readings inside the gap are never
//     visited directly. croner still fires a pattern that targets the skipped
//     hour, at T + (minute) — `0 2` → 03:00 EDT, `30 2` → 03:30 EDT,
//     `59 2` → 03:59 EDT — which is exactly `new Date(02:xx)` under V8's
//     shift-forward rule (instantOf with dateV8).
//
// Each reading therefore maps to instantOf(reading) and is compared against
// from's second floor (`floor`). The horizon (four years) bounds a search; an
// expression with no match inside answers "none".
func (c cronExpr) nextMatch(from time.Time, loc *time.Location) (time.Time, bool) {
	// croner's CronDate keeps milliseconds, drops seconds (CronDate(ms) has no
	// second field), and increment() adds one second. The first step therefore
	// is from's minute at :01 with from's milliseconds, so the walk starts at
	// from's own reading and the answer must be after from truncated to whole
	// seconds (`floor`). Two DST behaviours follow:
	//
	//   - spring forward: the walk steps 01:59 → 03:00 (V8 reads the skipped
	//     02:00 as 03:00) and then 03:00, 03:01, … A gap pattern fires once,
	//     at its V8-shifted instant (02:00/02:30/02:59 → 03:00/03:30/03:59),
	//     and the 03:xx readings are then produced normally (a later `0 3`
	//     matches at 03:00, `30 3` at 03:30, …).
	//   - fall back: the walk never produces the second pass of the repeated
	//     hour. When from sits in that second pass, its fields are the
	//     repeated hour and the first step reads :01 of the FIRST pass, so
	//     croner answers a first-pass reading up to one hour after from's
	//     reading (06:30Z → 01:31 EDT; 05:30Z → 02:30 EST, whose pattern has
	//     no later first-pass minute).
	start := wallOf(from, loc)
	horizon := start + wall(horizonDays*minutesPerDay)
	floor := from.UnixMilli() - int64(from.UnixMilli()%1000)
	rewindFloor := floor - int64(time.Hour/time.Millisecond)
	// rewind is true when from's own reading resolves to an earlier instant
	// than from's minute — i.e. from sits in the SECOND pass of a repeated
	// hour, whose fields still name the first-pass minute.
	readingInstant := instantOf(start, loc, dateV8).UnixMilli()
	minuteFloor := floor - int64(floor%60000)
	rewind := readingInstant < minuteFloor
	for current := start; current < horizon; current++ {
		if !c.dayMatches(current) {
			// No reading of this day qualifies: resume at the next midnight.
			current = current.dayStart().addDays(1) - 1
			continue
		}
		if !c.timeMatches(current) {
			continue
		}
		instant := c.cronTimeInstant(current, loc)
		key := instant.UnixMilli()
		if key > floor || (rewind && key > rewindFloor) {
			return instant, true
		}
	}
	return time.Time{}, false
}

// previousMatch returns the latest match at or before from: the last reading
// (of the same wall day, then earlier days) whose instant is not after from.
func (c cronExpr) previousMatch(from time.Time, loc *time.Location) (time.Time, bool) {
	readings := wallOf(from, loc)
	horizon := readings - wall(horizonDays*minutesPerDay)
	lastDay := readings.dayStart()
	for day := lastDay; day >= horizon; day = day.addDays(-1) {
		if !c.dayMatches(day) {
			continue
		}
		last := day.addDays(1) - 1
		if day == lastDay {
			last = readings
		}
		if candidate, ok := c.latestMatch(day, last, from, loc); ok {
			return candidate, true
		}
	}
	return time.Time{}, false
}

// latestMatch scans one day backwards for a matching reading at or before
// ceiling whose instant is not after from.
func (c cronExpr) latestMatch(day, ceiling wall, from time.Time, loc *time.Location) (time.Time, bool) {
	for current := ceiling; current >= day; current-- {
		if !c.timeMatches(current) {
			continue
		}
		instant := c.cronTimeInstant(current, loc)
		if instant.UnixMilli() > from.UnixMilli() {
			continue // second pass of a fall-back hour resolves to the first one
		}
		return instant, true
	}
	return time.Time{}, false
}

// timeMatches reports whether a reading's month, hour and minute match.
func (c cronExpr) timeMatches(w wall) bool {
	_, month, _, hour, minute := w.components()
	return c.months[int(month)] && c.hours[hour] && c.minutes[minute]
}

// cronTimeInstant is the instant croner's walk reaches for a matching reading:
// `new Date(fields)` under V8's rules (schedule.go). For a reading skipped by
// a spring-forward, that is the shift-forward instant the walk fires at.
func (c cronExpr) cronTimeInstant(reading wall, loc *time.Location) time.Time {
	return instantOf(reading, loc, dateV8)
}

// dayStart is midnight of w's date. It is computed from calendar fields, not
// by w%minutesPerDay: wall values before 1970 are negative multiples of a day
// and a plain remainder would round the wrong way.
func (w wall) dayStart() wall {
	year, month, day, _, _ := w.components()
	return wallFields(year, month, day, 0, 0)
}

// dayMatches applies croner's day rule: the month must be selected, and the
// day must satisfy day-of-month and/or day-of-week. With both restricted a
// day matching either qualifies; with one wildcard only the other decides.
func (c cronExpr) dayMatches(day wall) bool {
	year, month, dayOfMonth, _, _ := day.components()
	if !c.months[int(month)] {
		return false
	}
	domMatch := c.days[dayOfMonth]
	dowMatch := c.weekdays[int(time.Date(year, month, dayOfMonth, 0, 0, 0, 0, time.UTC).Weekday())]
	if c.domStar || c.dowStar {
		if c.domStar && c.dowStar {
			return true
		}
		if c.domStar {
			return dowMatch
		}
		return domMatch
	}
	return domMatch || dowMatch
}

// parseFields fills the field sets from the five cron fields. Semantics follow
// robfig (and croner): `*`/`?` wildcard, lists, ranges, steps and the standard
// month/weekday names; day-of-week accepts 0-7 with both 0 and 7 = Sunday.
func (expr *cronExpr) parseFields(fields []string) error {
	if err := expr.parseSet(fields[0], 0, 59, "minute", expr.minutes[:]); err != nil {
		return err
	}
	if err := expr.parseSet(fields[1], 0, 23, "hour", expr.hours[:]); err != nil {
		return err
	}
	if err := expr.parseSet(fields[2], 1, 31, "day-of-month", expr.days[1:]); err != nil {
		return err
	}
	if err := expr.parseSet(fields[3], 1, 12, "month", expr.months[1:]); err != nil {
		return err
	}
	var dow [8]bool // index 7 is Sunday's alias
	if err := expr.parseSet(fields[4], 0, 7, "day-of-week", dow[:]); err != nil {
		return err
	}
	for index := 0; index < 7; index++ {
		expr.weekdays[index] = dow[index]
	}
	if dow[7] {
		expr.weekdays[0] = true
	}
	return nil
}

var (
	monthNames = map[string]int{"jan": 1, "feb": 2, "mar": 3, "apr": 4, "may": 5, "jun": 6,
		"jul": 7, "aug": 8, "sep": 9, "oct": 10, "nov": 11, "dec": 12}
	weekdayNames = map[string]int{"sun": 0, "mon": 1, "tue": 2, "wed": 3, "thu": 4, "fri": 5, "sat": 6}
)

// parseSet records the values selected by one field into set (set[0] is min).
func (expr *cronExpr) parseSet(field string, min, max int, name string, set []bool) error {
	if field == "*" || field == "?" {
		for index := range set {
			set[index] = true
		}
		return nil
	}
	for _, part := range strings.Split(field, ",") {
		if err := expr.parsePart(part, min, max, name, set); err != nil {
			return err
		}
	}
	return nil
}

func (expr *cronExpr) parsePart(part string, min, max int, name string, set []bool) error {
	step := 1
	body := part
	if slash := strings.IndexByte(part, '/'); slash >= 0 {
		body = part[:slash]
		parsed, ok := parseInt64(part[slash+1:])
		if !ok || parsed < 1 {
			return fmt.Errorf("invalid %s step in %q", name, part)
		}
		step = int(parsed)
	}
	from, to := min, max
	switch {
	case body == "*" || body == "?":
		// wildcard span
	case strings.Contains(body, "-"):
		bounds := strings.SplitN(body, "-", 2)
		var ok bool
		from, ok = cronValue(bounds[0], min, name)
		if !ok {
			return fmt.Errorf("invalid %s range in %q", name, part)
		}
		to, ok = cronValue(bounds[1], min, name)
		if !ok {
			return fmt.Errorf("invalid %s range in %q", name, part)
		}
	default:
		value, ok := cronValue(body, min, name)
		if !ok {
			return fmt.Errorf("invalid %s value in %q", name, part)
		}
		from, to = value, value
		if step > 1 || strings.Contains(part, "/") {
			// `N/S` means "from N to max step S", like croner's numeric
			// prefix form.
			to = max
		}
	}
	if from > to {
		return fmt.Errorf("invalid %s range %q", name, part)
	}
	for value := from; value <= to && value-min < len(set); value += step {
		set[value-min] = true
	}
	return nil
}

// cronValue parses a numeric field value or a month/weekday name.
func cronValue(token string, min int, name string) (int, bool) {
	lower := strings.ToLower(token)
	if name == "month" {
		if month, ok := monthNames[lower]; ok {
			return month, true
		}
	}
	if name == "day-of-week" {
		if weekday, ok := weekdayNames[lower]; ok {
			return weekday, true
		}
	}
	value, ok := parseInt64(lower)
	if !ok {
		return 0, false
	}
	return int(value), true
}
