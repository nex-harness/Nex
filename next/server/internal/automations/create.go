package automations

import (
	"fmt"
	"strings"
	"time"
)

func initialNextRunAt(a *Automation, from time.Time, loc *time.Location) (time.Time, bool, error) {
	next, ok := nextRunAt(a, from, loc)
	if a.Recurring || a.ScheduleRule != nil || !fixedCalendarCron(a.CronExpr) {
		return next, ok, nil
	}
	expr, err := parseCronExpr(a.CronExpr)
	if err != nil {
		return next, ok, nil
	}
	previous, hasPrevious := expr.previous(from, loc)
	targetAgeKnown := hasPrevious && !isAfter(previous, from) && since(from, previous) <= oneShotStaleTargetWindowMs*int64(time.Millisecond)
	rolledToNextCalendar := !ok || since(next, from) > oneShotStaleTargetWindowMs*int64(time.Millisecond)
	if targetAgeKnown && rolledToNextCalendar {
		if since(from, previous) < oneShotMissedRunGraceMs*int64(time.Millisecond) {
			return from, true, nil
		}
		return time.Time{}, false, fmt.Errorf(
			"一次性定时任务的目标时间（%s）已过去；相对时间请使用 delayMinutes，绝对时间请确认未来时刻后重试",
			previous.In(loc).Format(time.DateTime),
		)
	}
	return next, ok, nil
}

// fixedCalendarCron reports `M H D MON *` (a one-shot calendar target).
func fixedCalendarCron(expr string) bool {
	fields := strings.Fields(strings.TrimSpace(expr))
	if len(fields) != 5 || fields[4] != "*" {
		return false
	}
	for _, field := range fields[:4] {
		if _, ok := parseInt64(field); !ok {
			return false
		}
	}
	return true
}

// normalizeLocation resolves an IANA zone, falling back to the server zone.
func normalizeLocation(name string, fallback *time.Location) *time.Location {
	if name != "" {
		if loc, err := time.LoadLocation(name); err == nil {
			return loc
		}
	}
	if fallback != nil {
		return fallback
	}
	return time.Local
}
