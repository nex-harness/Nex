package store

import (
	"context"
	"sort"
	"time"
)

// UsageWindow specifies the query horizon for Runner and Global analytics.
type UsageWindow string

const (
	WindowToday UsageWindow = "today"
	Window7D    UsageWindow = "7d"
	Window30D   UsageWindow = "30d"
)

// ModelUsageItem reports per-model consumption and estimated spend within a window.
type ModelUsageItem struct {
	ProviderID       string  `json:"provider_id"`
	ModelID          string  `json:"model_id"`
	TurnCount        int     `json:"turn_count"`
	InputTokens      int     `json:"input_tokens"`
	OutputTokens     int     `json:"output_tokens"`
	CacheReadTokens  int     `json:"cache_read_tokens"`
	CacheWriteTokens int     `json:"cache_write_tokens"`
	TotalTokens      int     `json:"total_tokens"`
	EstimatedCostUSD float64 `json:"estimated_cost_usd"`
	CacheSavedUSD    float64 `json:"cache_saved_usd"`
}

// UsageTimePoint represents a single hourly or daily time bucket in UsageSummary.Series.
type UsageTimePoint struct {
	BucketUnixSec    int64   `json:"bucket_unix_sec"`
	Label            string  `json:"label"`
	TurnCount        int     `json:"turn_count"`
	InputTokens      int     `json:"input_tokens"`
	OutputTokens     int     `json:"output_tokens"`
	CacheReadTokens  int     `json:"cache_read_tokens"`
	CacheWriteTokens int     `json:"cache_write_tokens"`
	TotalTokens      int     `json:"total_tokens"`
	EstimatedCostUSD float64 `json:"estimated_cost_usd"`
}

// UsageSummary aggregates token consumption, cache efficiency, model breakdown, and cost for a Runner or globally.
type UsageSummary struct {
	RunnerID         string           `json:"runner_id,omitempty"` // Empty means global across all runners
	Window           UsageWindow      `json:"window"`
	SinceUnixSec     int64            `json:"since_unix_sec"`
	TurnCount        int              `json:"turn_count"`
	InputTokens      int              `json:"input_tokens"`
	OutputTokens     int              `json:"output_tokens"`
	CacheReadTokens  int              `json:"cache_read_tokens"`
	CacheWriteTokens int              `json:"cache_write_tokens"`
	TotalTokens      int              `json:"total_tokens"`
	CacheHitRatePct  float64          `json:"cache_hit_rate_pct"`
	EstimatedCostUSD float64          `json:"estimated_cost_usd"`
	CacheSavedUSD    float64          `json:"cache_saved_usd"`
	BySourceTokens   map[string]int   `json:"by_source_tokens"`
	Models           []ModelUsageItem `json:"models"`
	Series           []UsageTimePoint `json:"series"`
}

func windowStartUnix(window UsageWindow, now time.Time, loc *time.Location) int64 {
	if loc == nil {
		loc = time.Local
	}
	localNow := now.In(loc)
	todayMidnight := time.Date(localNow.Year(), localNow.Month(), localNow.Day(), 0, 0, 0, 0, loc)

	var start time.Time
	switch window {
	case WindowToday:
		start = todayMidnight
	case Window7D:
		start = todayMidnight.AddDate(0, 0, -6)
	case Window30D:
		start = todayMidnight.AddDate(0, 0, -29)
	default:
		start = todayMidnight
	}
	sec := start.Unix()
	return sec - (sec % 3600)
}

// QueryUsageSummary computes fast hourly-rollup statistics for a specific runnerID (or all runners when runnerID == "").
func (s *Store) QueryUsageSummary(ctx context.Context, runnerID string, window UsageWindow, now time.Time, loc *time.Location) (*UsageSummary, error) {
	sinceBucket := windowStartUnix(window, now, loc)

	q := `
		SELECT provider_id, model_id, trigger_source,
		       SUM(turn_count), SUM(input_tokens), SUM(output_tokens),
		       SUM(cache_read_tokens), SUM(cache_write_tokens),
		       SUM(estimated_cost_usd), SUM(cache_saved_usd)
		FROM usage_rollups_hourly
		WHERE hour_bucket >= ?
	`
	args := []any{sinceBucket}
	if runnerID != "" {
		q += ` AND runner_id = ?`
		args = append(args, runnerID)
	}
	q += ` GROUP BY provider_id, model_id, trigger_source`

	rows, err := s.mainDB.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	summary := &UsageSummary{
		RunnerID:       runnerID,
		Window:         window,
		SinceUnixSec:   sinceBucket,
		BySourceTokens: map[string]int{"chat": 0, "subagent": 0, "cron": 0},
	}

	type modelKey struct{ prov, mod string }
	modelMap := make(map[modelKey]*ModelUsageItem)

	for rows.Next() {
		var prov, mod, src string
		var turns, inTok, outTok, cRead, cWrite int
		var cost, saved float64
		if err := rows.Scan(&prov, &mod, &src, &turns, &inTok, &outTok, &cRead, &cWrite, &cost, &saved); err != nil {
			return nil, err
		}
		tot := inTok + outTok + cRead + cWrite

		summary.TurnCount += turns
		summary.InputTokens += inTok
		summary.OutputTokens += outTok
		summary.CacheReadTokens += cRead
		summary.CacheWriteTokens += cWrite
		summary.TotalTokens += tot
		summary.EstimatedCostUSD += cost
		summary.CacheSavedUSD += saved
		summary.BySourceTokens[src] += tot

		mk := modelKey{prov: prov, mod: mod}
		item := modelMap[mk]
		if item == nil {
			item = &ModelUsageItem{ProviderID: prov, ModelID: mod}
			modelMap[mk] = item
		}
		item.TurnCount += turns
		item.InputTokens += inTok
		item.OutputTokens += outTok
		item.CacheReadTokens += cRead
		item.CacheWriteTokens += cWrite
		item.TotalTokens += tot
		item.EstimatedCostUSD += cost
		item.CacheSavedUSD += saved
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	totalPromptTokens := summary.InputTokens + summary.CacheReadTokens + summary.CacheWriteTokens
	if totalPromptTokens > 0 {
		summary.CacheHitRatePct = float64(summary.CacheReadTokens) * 100.0 / float64(totalPromptTokens)
	}

	summary.Models = make([]ModelUsageItem, 0, len(modelMap))
	for _, it := range modelMap {
		summary.Models = append(summary.Models, *it)
	}
	sort.Slice(summary.Models, func(i, j int) bool {
		if summary.Models[i].EstimatedCostUSD != summary.Models[j].EstimatedCostUSD {
			return summary.Models[i].EstimatedCostUSD > summary.Models[j].EstimatedCostUSD
		}
		return summary.Models[i].TotalTokens > summary.Models[j].TotalTokens
	})

	summary.Series = s.buildUsageSeries(ctx, runnerID, window, sinceBucket, now, loc)

	return summary, nil
}

func (s *Store) buildUsageSeries(ctx context.Context, runnerID string, window UsageWindow, sinceBucket int64, now time.Time, loc *time.Location) []UsageTimePoint {
	if loc == nil {
		loc = time.Local
	}
	localNow := now.In(loc)
	todayMidnight := time.Date(localNow.Year(), localNow.Month(), localNow.Day(), 0, 0, 0, 0, loc)

	hourly := window != Window7D && window != Window30D
	var slots []UsageTimePoint
	slotIndexByKey := make(map[string]int)

	if hourly {
		slots = make([]UsageTimePoint, 0, 24)
		for i := 0; i < 24; i++ {
			st := todayMidnight.Add(time.Duration(i) * time.Hour)
			key := st.Format("2006-01-02T15")
			slotIndexByKey[key] = len(slots)
			slots = append(slots, UsageTimePoint{
				BucketUnixSec: st.Unix(),
				Label:         st.Format("15:04"),
			})
		}
	} else {
		days := 7
		if window == Window30D {
			days = 30
		}
		slots = make([]UsageTimePoint, 0, days)
		for i := days - 1; i >= 0; i-- {
			st := todayMidnight.AddDate(0, 0, -i)
			key := st.Format("2006-01-02")
			slotIndexByKey[key] = len(slots)
			slots = append(slots, UsageTimePoint{
				BucketUnixSec: st.Unix(),
				Label:         st.Format("01-02"),
			})
		}
	}

	q := `
		SELECT hour_bucket,
		       SUM(turn_count), SUM(input_tokens), SUM(output_tokens),
		       SUM(cache_read_tokens), SUM(cache_write_tokens),
		       SUM(estimated_cost_usd)
		FROM usage_rollups_hourly
		WHERE hour_bucket >= ?
	`
	args := []any{sinceBucket}
	if runnerID != "" {
		q += ` AND runner_id = ?`
		args = append(args, runnerID)
	}
	q += ` GROUP BY hour_bucket ORDER BY hour_bucket ASC`

	rows, err := s.mainDB.QueryContext(ctx, q, args...)
	if err != nil {
		return slots
	}
	defer rows.Close()

	for rows.Next() {
		var hourBucket int64
		var turns, inTok, outTok, cRead, cWrite int
		var cost float64
		if err := rows.Scan(&hourBucket, &turns, &inTok, &outTok, &cRead, &cWrite, &cost); err != nil {
			continue
		}
		bt := time.Unix(hourBucket, 0).In(loc)
		var key string
		if hourly {
			key = bt.Format("2006-01-02T15")
		} else {
			key = bt.Format("2006-01-02")
		}
		idx, ok := slotIndexByKey[key]
		if !ok {
			continue
		}
		tot := inTok + outTok + cRead + cWrite
		slots[idx].TurnCount += turns
		slots[idx].InputTokens += inTok
		slots[idx].OutputTokens += outTok
		slots[idx].CacheReadTokens += cRead
		slots[idx].CacheWriteTokens += cWrite
		slots[idx].TotalTokens += tot
		slots[idx].EstimatedCostUSD += cost
	}
	return slots
}
