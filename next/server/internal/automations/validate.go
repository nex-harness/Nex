package automations

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"
)

// The error texts below are the ones packages/services/src/session/
// automationService.ts throws. The UI maps them to toasts, so they must keep
// their exact shape (docs/next/automations-views-search.md §A).

// ErrCronExpr is InvalidCronExprError: `非法的 cron 表达式：<expr>`.
func invalidCronExpr(expr string) error {
	return fmt.Errorf("非法的 cron 表达式：%s", expr)
}

// invalidScheduleRule prefixes the rule message like
// InvalidAutomationScheduleRuleError. It is kept unexported because only
// callers inside this package construct it; the message is what matters.
func invalidScheduleRule(message string) error {
	return fmt.Errorf("非法的定时任务调度规则：%s", message)
}

// invalidRelativeDelay mirrors InvalidAutomationRelativeDelayError.
func invalidRelativeDelay(message string) error {
	return fmt.Errorf("非法的相对时间定时任务：%s", message)
}

// invalidIntervalCarrier mirrors InvalidAutomationIntervalCarrierError.
func invalidIntervalCarrier(message string) error {
	return fmt.Errorf("非法的自定义重复入参：%s", message)
}

// createLimitError mirrors AutomationCreateLimitError. The code lives in the
// message so isAutomationCreateLimitError in the UI detects it.
func createLimitError() error {
	return fmt.Errorf("[%s] At most %d automations may be retained. Delete an existing automation before creating another.", CreateLimitErrorCode, CreateLimit)
}

// invalidMaxRunsUpdate mirrors InvalidAutomationMaxRunsUpdateError.
func invalidMaxRunsUpdate(message string) error {
	if message == "" {
		message = "清空 maxRuns 时必须在同一次更新中设置 recurring=true"
	}
	return errors.New(message)
}

const (
	maxMonthlyScheduleInterval = 1200
	maxSessionInterval         = 200
	maxRelativeDelayMinutes    = 525600
	oneShotMissedRunGraceMs    = 60 * 1000
	oneShotStaleTargetWindowMs = 30 * 60 * 1000
)

// CreateParams is NexAutomationCreateParams as the channel receives it.
type CreateParams struct {
	Title                string          `json:"title"`
	CronExpr             string          `json:"cronExpr"`
	RelativeDelayMinutes *float64        `json:"relativeDelayMinutes,omitempty"`
	Prompt               string          `json:"prompt"`
	ModelSelection       json.RawMessage `json:"modelSelection,omitempty"`
	Mode                 *string         `json:"mode,omitempty"`
	WorkspacePath        string          `json:"workspacePath"`
	WorkspaceIdentity    string          `json:"workspaceIdentity,omitempty"`
	TargetTaskID         string          `json:"targetTaskId,omitempty"`
	Recurring            *bool           `json:"recurring,omitempty"`
	MaxRuns              *float64        `json:"maxRuns,omitempty"`
	EndAt                *float64        `json:"endAt,omitempty"`
	ScheduleRule         json.RawMessage `json:"scheduleRule,omitempty"`
	IntervalUnit         string          `json:"intervalUnit,omitempty"`
	Interval             *float64        `json:"interval,omitempty"`
}

// UpdateParams is NexAutomationUpdateParams. Raw fields distinguish
// "undefined" (absent) from "null" (explicit clear) because the old contract
// depends on that difference.
type UpdateParams struct {
	Title                *string         `json:"title,omitempty"`
	CronExpr             *string         `json:"cronExpr,omitempty"`
	Prompt               *string         `json:"prompt,omitempty"`
	ModelSelection       json.RawMessage `json:"modelSelection,omitempty"`
	Mode                 json.RawMessage `json:"mode,omitempty"`
	Recurring            *bool           `json:"recurring,omitempty"`
	MaxRuns              json.RawMessage `json:"maxRuns,omitempty"`
	EndAt                json.RawMessage `json:"endAt,omitempty"`
	ScheduleRule         json.RawMessage `json:"scheduleRule,omitempty"`
	IntervalUnit         string          `json:"intervalUnit,omitempty"`
	Interval             *float64        `json:"interval,omitempty"`
	ScheduleEditedByUser *bool           `json:"scheduleEditedByUser,omitempty"`
}

// isUndefined reports whether a raw field was omitted (the JSON bridge
// encodes JavaScript undefined as {"$undef":true}; the channel unwraps that to
// a nil RawMessage, and the test surface accepts it here too).
func isUndefined(raw json.RawMessage) bool {
	if len(raw) == 0 {
		return true
	}
	var probe map[string]any
	if err := json.Unmarshal(raw, &probe); err == nil && len(probe) == 1 {
		if value, ok := probe["$undef"]; ok {
			if flag, isBool := value.(bool); isBool && flag {
				return true
			}
		}
	}
	return false
}

func isNull(raw json.RawMessage) bool {
	return len(raw) > 0 && strings.TrimSpace(string(raw)) == "null"
}

// decodeRule decodes a scheduleRule value.
func decodeRule(raw json.RawMessage) (*ScheduleRule, error) {
	if isUndefined(raw) || isNull(raw) {
		return nil, nil
	}
	var rule ScheduleRule
	if err := json.Unmarshal(raw, &rule); err != nil {
		return nil, invalidScheduleRule("rule 结构无效")
	}
	return &rule, nil
}

// validScheduleRule ports assertValidAutomationScheduleRule.
func validScheduleRule(rule *ScheduleRule) error {
	switch rule.Unit {
	case UnitMinute, UnitHourly, UnitDaily, UnitWeekly, UnitMonthly, UnitYearly:
	default:
		return invalidScheduleRule("unit 不受支持")
	}
	if rule.Interval != math.Trunc(rule.Interval) || rule.Interval < 1 {
		return invalidScheduleRule("interval 必须是正整数")
	}
	if rule.Unit == UnitMonthly && rule.Interval > maxMonthlyScheduleInterval {
		return invalidScheduleRule(fmt.Sprintf("monthly interval 不能超过 %d", maxMonthlyScheduleInterval))
	}
	if rule.Hour != math.Trunc(rule.Hour) || rule.Hour < 0 || rule.Hour > 23 {
		return invalidScheduleRule("hour 必须是 0-23 的整数")
	}
	if rule.Minute != math.Trunc(rule.Minute) || rule.Minute < 0 || rule.Minute > 59 {
		return invalidScheduleRule("minute 必须是 0-59 的整数")
	}
	if rule.MonthlyMode != "" && rule.MonthlyMode != "date" && rule.MonthlyMode != "weekday" {
		return invalidScheduleRule("monthlyMode 不受支持")
	}
	if rule.Weekdays != nil && !integersInRange(rule.Weekdays, 0, 6) {
		return invalidScheduleRule("weekdays 必须是 0-6 的非空整数数组")
	}
	if rule.Unit == UnitWeekly && !integersInRange(rule.Weekdays, 0, 6) {
		return invalidScheduleRule("weekly 规则必须包含有效的 weekdays")
	}
	if rule.Unit == UnitMonthly {
		if rule.MonthlyMode == "weekday" && !integersInRange(rule.Weekdays, 0, 6) {
			return invalidScheduleRule("monthly weekday 规则必须包含有效的 weekdays")
		}
		if rule.MonthlyMode != "weekday" && !integersInRange(rule.MonthDays, 1, 31) {
			return invalidScheduleRule("monthly date 规则必须包含有效的 monthDays")
		}
	}
	if rule.Months != nil && !integersInRange(rule.Months, 1, 12) {
		return invalidScheduleRule("months 必须是 1-12 的非空整数数组")
	}
	if rule.MonthDays != nil && !integersInRange(rule.MonthDays, 1, 31) {
		return invalidScheduleRule("monthDays 必须是 1-31 的非空整数数组")
	}
	return nil
}

func integersInRange(values []int, min, max int) bool {
	if len(values) == 0 {
		return false
	}
	for _, value := range values {
		if value < min || value > max {
			return false
		}
	}
	return true
}

// scheduleRuleDefinition mirrors scheduleRuleDefinition: the fields that
// identify a rule, ignoring anchorAt.
func scheduleRuleDefinition(rule *ScheduleRule) string {
	encode := func(values []int) any {
		if values == nil {
			return nil
		}
		sorted := append([]int(nil), values...)
		sortInts(sorted)
		return sorted
	}
	monthlyMode := any(nil)
	if rule.MonthlyMode != "" {
		monthlyMode = rule.MonthlyMode
	}
	encoded, _ := json.Marshal([]any{
		rule.Unit, rule.Interval, rule.Hour, rule.Minute,
		encode(rule.Weekdays), encode(rule.MonthDays), encode(rule.Months), monthlyMode,
	})
	return string(encoded)
}

func sortInts(values []int) {
	for i := 1; i < len(values); i++ {
		for j := i; j > 0 && values[j] < values[j-1]; j-- {
			values[j], values[j-1] = values[j-1], values[j]
		}
	}
}

// validateIntervalCarrier ports assertValidAutomationIntervalCarrier.
// initialNextRunAt ports computeInitialAutomationNextRunAt. A one-shot fixed
// calendar cron whose target minute was just missed must not roll to next
// year: within one minute it runs immediately, between one and thirty
// minutes it is rejected as stale, beyond that next-year semantics stay.
// resolvedCreate is a validated CreateParams ready to persist.
type resolvedCreate struct {
	title          string
	cronExpr       string
	prompt         string
	modelSelection *ModelSelection
	mode           *string
	scheduleRule   *ScheduleRule
	recurring      bool
	maxRuns        *int
	endAt          *int64
	nextRunAt      *int64
	lifecycle      string
}

// resolveCreate ports AutomationService.create.
func resolveCreate(params CreateParams, now time.Time, loc *time.Location) (resolvedCreate, error) {
	title := strings.TrimSpace(params.Title)
	if title == "" {
		return resolvedCreate{}, errors.New("title is required")
	}
	if strings.TrimSpace(params.WorkspacePath) == "" {
		return resolvedCreate{}, errors.New("workspacePath is required")
	}
	if strings.TrimSpace(params.Prompt) == "" {
		return resolvedCreate{}, errors.New("prompt is required")
	}
	recurring := true
	if params.Recurring != nil {
		recurring = *params.Recurring
	}
	if params.RelativeDelayMinutes != nil {
		delay := *params.RelativeDelayMinutes
		if delay != math.Trunc(delay) || delay < 1 || delay > maxRelativeDelayMinutes {
			return resolvedCreate{}, invalidRelativeDelay("delayMinutes 必须是 1-525600 的整数")
		}
		if recurring {
			return resolvedCreate{}, invalidRelativeDelay("相对延迟任务必须设置 recurring=false")
		}
		if params.MaxRuns != nil {
			return resolvedCreate{}, invalidRelativeDelay("相对延迟任务固定只运行一次，不能设置 maxRuns")
		}
		if len(params.ScheduleRule) > 0 && !isUndefined(params.ScheduleRule) && !isNull(params.ScheduleRule) {
			return resolvedCreate{}, invalidRelativeDelay("不能同时提交 delayMinutes 和 scheduleRule")
		}
	}
	directRule, err := decodeRule(params.ScheduleRule)
	if err != nil {
		return resolvedCreate{}, err
	}
	if err := validateIntervalCarrier(
		params.IntervalUnit, params.Interval, directRule != nil,
		params.RelativeDelayMinutes, params.Recurring, params.MaxRuns,
	); err != nil {
		return resolvedCreate{}, err
	}

	cronExprValue := params.CronExpr
	var rule *ScheduleRule
	if params.RelativeDelayMinutes != nil {
		cronExprValue, rule = buildRelativeDelaySchedule(int(*params.RelativeDelayMinutes), now, loc)
		recurring = false
	}
	if params.IntervalUnit != "" {
		rule, err = buildIntervalScheduleRule(params.IntervalUnit, *params.Interval, cronExprValue, now, loc)
		if err != nil {
			return resolvedCreate{}, err
		}
		// The carrier is an unbounded period by definition.
		recurring = true
		params.MaxRuns = nil
	} else {
		rule = directRule
	}
	if _, err := parseCronExpr(cronExprValue); err != nil {
		return resolvedCreate{}, invalidCronExpr(params.CronExpr)
	}
	if rule != nil {
		if err := validScheduleRule(rule); err != nil {
			return resolvedCreate{}, err
		}
		// Direct rules were normalized above; always re-anchor to the real
		// creation instant like the service does.
		anchored := *rule
		anchored.AnchorAt = float64(now.UnixMilli())
		rule = &anchored
	} else {
		rule = inferMinuteIntervalScheduleRule(cronExprValue, now, loc)
	}

	automation := &Automation{CronExpr: cronExprValue, ScheduleRule: rule, Recurring: recurring}
	next, hasNext, err := initialNextRunAt(automation, now, loc)
	if err != nil {
		return resolvedCreate{}, err
	}
	var nextMillis *int64
	lifecycle := LifecycleActive
	var endAt *int64
	if params.EndAt != nil {
		value := int64(*params.EndAt)
		endAt = &value
	}
	if hasNext {
		value := next.UnixMilli()
		nextMillis = &value
	}
	if endAt != nil && (!hasNext || *nextMillis > *endAt) {
		nextMillis = nil
		lifecycle = LifecycleCompleted
	}
	var maxRuns *int
	if params.MaxRuns != nil {
		value := int(*params.MaxRuns)
		maxRuns = &value
	}
	selection, err := decodeSelection(params.ModelSelection)
	if err != nil {
		return resolvedCreate{}, err
	}
	return resolvedCreate{
		title:          title,
		cronExpr:       cronExprValue,
		prompt:         params.Prompt,
		modelSelection: selection,
		mode:           params.Mode,
		scheduleRule:   rule,
		recurring:      recurring,
		maxRuns:        maxRuns,
		endAt:          endAt,
		nextRunAt:      nextMillis,
		lifecycle:      lifecycle,
	}, nil
}

// decodeSelection decodes a modelSelection value (undefined/null → nil).
func decodeSelection(raw json.RawMessage) (*ModelSelection, error) {
	if isUndefined(raw) || isNull(raw) {
		return nil, nil
	}
	var selection ModelSelection
	if err := json.Unmarshal(raw, &selection); err != nil {
		return nil, fmt.Errorf("invalid modelSelection: %w", err)
	}
	if selection.ProviderID == "" || selection.ModelID == "" {
		return nil, errors.New("invalid modelSelection: providerId and modelId are required")
	}
	return &selection, nil
}
