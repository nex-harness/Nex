package automations

import (
	"fmt"
	"math"
	"strings"
	"time"
)

func validateIntervalCarrier(intervalUnit string, interval *float64, hasScheduleRule bool, relativeDelay *float64, recurring *bool, maxRuns *float64) error {
	unitPresent := intervalUnit != ""
	if unitPresent != (interval != nil) {
		return invalidIntervalCarrier("intervalUnit 与 interval 必须同时提交或同时省略")
	}
	if interval != nil && (*interval != math.Trunc(*interval) || *interval < 1 || *interval > maxSessionInterval) {
		return invalidIntervalCarrier("interval 必须是 1-200 的整数")
	}
	if unitPresent && relativeDelay != nil {
		return invalidIntervalCarrier("intervalUnit 是周期 carrier，不能与一次性 relativeDelayMinutes 同时提交")
	}
	if unitPresent && hasScheduleRule {
		return invalidIntervalCarrier("intervalUnit carrier 不能与直传 scheduleRule 同时提交")
	}
	if unitPresent && recurring != nil && !*recurring {
		return invalidIntervalCarrier("intervalUnit carrier 必须使用 recurring=true")
	}
	if unitPresent && maxRuns != nil {
		return invalidIntervalCarrier("intervalUnit carrier 不能与有限 maxRuns 同时提交")
	}
	return nil
}

// buildIntervalScheduleRule ports buildIntervalScheduleRule: the carrier
// interval is authoritative; the compatibility cron only supplies the
// hour/minute/weekdays/monthDays/months.
func buildIntervalScheduleRule(intervalUnit string, interval float64, cronExprValue string, anchor time.Time, loc *time.Location) (*ScheduleRule, error) {
	fields := parseCronFields(cronExprValue)
	local := anchor.In(loc)
	minute := float64(local.Minute())
	if fields.minute != nil {
		minute = *fields.minute
	}
	hour := float64(local.Hour())
	if fields.hour != nil {
		hour = *fields.hour
	}
	monthDays := fields.monthDays
	if monthDays == nil {
		monthDays = []int{local.Day()}
	}
	months := fields.months
	if months == nil {
		months = []int{int(local.Month())}
	}
	weekdays := fields.weekdays
	if weekdays == nil {
		weekdays = []int{1}
	}
	anchorAt := float64(anchor.UnixMilli())
	switch intervalUnit {
	case UnitMinute:
		return &ScheduleRule{Unit: UnitMinute, Interval: interval, Hour: float64(local.Hour()), Minute: float64(local.Minute()), AnchorAt: anchorAt}, nil
	case UnitHourly:
		return &ScheduleRule{Unit: UnitHourly, Interval: interval, Hour: 0, Minute: minute, AnchorAt: anchorAt}, nil
	case UnitDaily:
		return &ScheduleRule{Unit: UnitDaily, Interval: interval, Hour: hour, Minute: minute, AnchorAt: anchorAt}, nil
	case UnitWeekly:
		return &ScheduleRule{Unit: UnitWeekly, Interval: interval, Hour: hour, Minute: minute, Weekdays: weekdays, AnchorAt: anchorAt}, nil
	case UnitMonthly:
		return &ScheduleRule{Unit: UnitMonthly, Interval: interval, Hour: hour, Minute: minute, MonthDays: monthDays, MonthlyMode: "date", AnchorAt: anchorAt}, nil
	case UnitYearly:
		return &ScheduleRule{Unit: UnitYearly, Interval: interval, Hour: hour, Minute: minute, Months: months, MonthDays: monthDays, AnchorAt: anchorAt}, nil
	}
	return nil, fmt.Errorf("Unsupported intervalUnit: %s", intervalUnit)
}

// cronFields holds the numeric fields parseCronFields extracts.
type cronFields struct {
	minute    *float64
	hour      *float64
	monthDays []int
	months    []int
	weekdays  []int
}

// parseCronFields ports parseCronFields: only plain numeric fields and comma
// lists are interpreted; steps/nth expressions fall back to defaults.
func parseCronFields(expr string) cronFields {
	parts := strings.Fields(strings.TrimSpace(expr))
	if len(parts) < 5 {
		return cronFields{}
	}
	single := func(token string) *float64 {
		if token == "*" || token == "?" {
			return nil
		}
		value, err := parseInt64(token)
		if !err {
			return nil
		}
		v := float64(value)
		return &v
	}
	list := func(token string) []int {
		if token == "*" || token == "?" {
			return nil
		}
		var values []int
		for _, piece := range strings.Split(token, ",") {
			value, ok := parseInt64(piece)
			if !ok {
				continue
			}
			values = append(values, int(value))
		}
		if len(values) == 0 {
			return nil
		}
		return values
	}
	return cronFields{
		minute:    single(parts[0]),
		hour:      single(parts[1]),
		monthDays: list(parts[2]),
		months:    list(parts[3]),
		weekdays:  list(parts[4]),
	}
}

// buildRelativeDelaySchedule ports buildRelativeDelaySchedule: a one-shot
// rule anchored to the server clock with a display cron.
func buildRelativeDelaySchedule(delayMinutes int, from time.Time, loc *time.Location) (string, *ScheduleRule) {
	target := from.Add(time.Duration(delayMinutes) * time.Minute).In(loc)
	cronText := fmt.Sprintf("%d %d %d %d *", target.Minute(), target.Hour(), target.Day(), int(target.Month()))
	return cronText, &ScheduleRule{
		Unit:     UnitMinute,
		Interval: float64(delayMinutes),
		Hour:     float64(target.Hour()),
		Minute:   float64(target.Minute()),
		AnchorAt: float64(from.UnixMilli()),
	}
}
