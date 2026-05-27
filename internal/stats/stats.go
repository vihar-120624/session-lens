// Package stats provides read-only aggregate queries over the sessions table:
// current-month summary, daily/weekly buckets, hourly granular series, per-model
// breakdowns, top-project rollups, and spike detection.
package stats

import (
	"database/sql"
	"fmt"
	"sort"
	"strings"
	"time"
)

// Summary is the current-calendar-month rollup.
type Summary struct {
	TotalInput         int64   `json:"total_input"`
	TotalOutput        int64   `json:"total_output"`
	TotalCacheRead     int64   `json:"total_cache_read"`
	TotalCacheWrite    int64   `json:"total_cache_write"`
	TotalTokens        int64   `json:"total_tokens"`
	TotalCostUSD       float64 `json:"total_cost_usd"`
	SessionCount       int64   `json:"session_count"`
	PlanBudgetUSD      float64 `json:"plan_budget_usd"`
	PlanUtilisationPct float64 `json:"plan_utilisation_pct"`
}

// Bucket is a generic time-bucket aggregate (used for daily and weekly).
type Bucket struct {
	Bucket           string  `json:"bucket"`
	InputTokens      int64   `json:"input_tokens"`
	OutputTokens     int64   `json:"output_tokens"`
	CacheReadTokens  int64   `json:"cache_read_tokens"`
	CacheWriteTokens int64   `json:"cache_write_tokens"`
	TotalTokens      int64   `json:"total_tokens"`
	TotalCostUSD     float64 `json:"total_cost_usd"`
	SessionCount     int64   `json:"session_count"`
}

// Project is a per-project rollup row.
type Project struct {
	ProjectPath      string  `json:"project_path"`
	InputTokens      int64   `json:"input_tokens"`
	OutputTokens     int64   `json:"output_tokens"`
	CacheReadTokens  int64   `json:"cache_read_tokens"`
	CacheWriteTokens int64   `json:"cache_write_tokens"`
	TotalTokens      int64   `json:"total_tokens"`
	TotalCostUSD     float64 `json:"total_cost_usd"`
	SessionCount     int64   `json:"session_count"`
}

// ModelTotal is a per-family rollup across all sessions.
type ModelTotal struct {
	Family           string  `json:"family"`
	InputTokens      int64   `json:"input_tokens"`
	OutputTokens     int64   `json:"output_tokens"`
	CacheReadTokens  int64   `json:"cache_read_tokens"`
	CacheWriteTokens int64   `json:"cache_write_tokens"`
	TotalTokens      int64   `json:"total_tokens"`
	TotalCostUSD     float64 `json:"total_cost_usd"`
	SessionCount     int64   `json:"session_count"`
}

// DailyByModel is one date's tokens broken down by family.
type DailyByModel struct {
	Bucket string           `json:"bucket"`
	Totals map[string]int64 `json:"totals"` // family -> tokens
}

// ByModelResponse is what /v1/stats/by-model returns: family totals plus the
// daily stacked-bar series.
type ByModelResponse struct {
	Totals []ModelTotal   `json:"totals"`
	Daily  []DailyByModel `json:"daily"`
}

// MonthSummary aggregates sessions ended in the current UTC calendar month.
func MonthSummary(conn *sql.DB, planBudget float64) (Summary, error) {
	return monthSummaryAt(conn, planBudget, time.Now().UTC())
}

func monthSummaryAt(conn *sql.DB, planBudget float64, now time.Time) (Summary, error) {
	monthStart := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC)
	const q = `
SELECT
  COALESCE(SUM(input_tokens),0),
  COALESCE(SUM(output_tokens),0),
  COALESCE(SUM(cache_read_tokens),0),
  COALESCE(SUM(cache_write_tokens),0),
  COALESCE(SUM(total_cost_usd),0),
  COUNT(1)
FROM sessions
WHERE ended_at >= ?
`
	var s Summary
	err := conn.QueryRow(q, monthStart.Format(time.RFC3339)).Scan(
		&s.TotalInput, &s.TotalOutput, &s.TotalCacheRead, &s.TotalCacheWrite,
		&s.TotalCostUSD, &s.SessionCount,
	)
	if err != nil {
		return Summary{}, fmt.Errorf("month summary: %w", err)
	}
	s.TotalTokens = s.TotalInput + s.TotalOutput + s.TotalCacheRead + s.TotalCacheWrite
	s.PlanBudgetUSD = planBudget
	if planBudget > 0 {
		s.PlanUtilisationPct = (s.TotalCostUSD / planBudget) * 100.0
		if s.PlanUtilisationPct > 999.9 {
			s.PlanUtilisationPct = 999.9
		}
	}
	return s, nil
}

// Daily returns one bucket per day for the last `days` days (newest last).
func Daily(conn *sql.DB, days int) ([]Bucket, error) {
	if days <= 0 {
		days = 30
	}
	if days > 365 {
		days = 365
	}
	since := time.Now().UTC().AddDate(0, 0, -days+1)
	cutoff := time.Date(since.Year(), since.Month(), since.Day(), 0, 0, 0, 0, time.UTC)
	const q = `
SELECT
  substr(ended_at, 1, 10) AS bucket,
  COALESCE(SUM(input_tokens),0),
  COALESCE(SUM(output_tokens),0),
  COALESCE(SUM(cache_read_tokens),0),
  COALESCE(SUM(cache_write_tokens),0),
  COALESCE(SUM(total_cost_usd),0),
  COUNT(1)
FROM sessions
WHERE ended_at >= ?
GROUP BY bucket
ORDER BY bucket ASC
`
	return queryBuckets(conn, q, cutoff.Format(time.RFC3339))
}

// Weekly returns one ISO-week bucket per week for the last `weeks` weeks.
func Weekly(conn *sql.DB, weeks int) ([]Bucket, error) {
	if weeks <= 0 {
		weeks = 12
	}
	if weeks > 104 {
		weeks = 104
	}
	since := time.Now().UTC().AddDate(0, 0, -7*weeks+1)
	cutoff := time.Date(since.Year(), since.Month(), since.Day(), 0, 0, 0, 0, time.UTC)
	// SQLite strftime('%Y-%W', ...) gives ISO-ish year-week pairs.
	const q = `
SELECT
  strftime('%Y-W%W', ended_at) AS bucket,
  COALESCE(SUM(input_tokens),0),
  COALESCE(SUM(output_tokens),0),
  COALESCE(SUM(cache_read_tokens),0),
  COALESCE(SUM(cache_write_tokens),0),
  COALESCE(SUM(total_cost_usd),0),
  COUNT(1)
FROM sessions
WHERE ended_at >= ?
GROUP BY bucket
ORDER BY bucket ASC
`
	return queryBuckets(conn, q, cutoff.Format(time.RFC3339))
}

// Hourly returns one bucket per UTC hour for the last `days` days. The label
// format is "YYYY-MM-DD HH" so the UI can plot a time-series directly.
func Hourly(conn *sql.DB, days int) ([]Bucket, error) {
	if days <= 0 {
		days = 7
	}
	if days > 90 {
		days = 90
	}
	since := time.Now().UTC().AddDate(0, 0, -days+1)
	cutoff := time.Date(since.Year(), since.Month(), since.Day(), 0, 0, 0, 0, time.UTC)
	const q = `
SELECT
  strftime('%Y-%m-%d %H', ended_at) AS bucket,
  COALESCE(SUM(input_tokens),0),
  COALESCE(SUM(output_tokens),0),
  COALESCE(SUM(cache_read_tokens),0),
  COALESCE(SUM(cache_write_tokens),0),
  COALESCE(SUM(total_cost_usd),0),
  COUNT(1)
FROM sessions
WHERE ended_at >= ?
GROUP BY bucket
ORDER BY bucket ASC
`
	return queryBuckets(conn, q, cutoff.Format(time.RFC3339))
}

func queryBuckets(conn *sql.DB, q string, args ...any) ([]Bucket, error) {
	rows, err := conn.Query(q, args...)
	if err != nil {
		return nil, fmt.Errorf("query buckets: %w", err)
	}
	defer rows.Close()

	out := make([]Bucket, 0, 32)
	for rows.Next() {
		var b Bucket
		if err := rows.Scan(
			&b.Bucket, &b.InputTokens, &b.OutputTokens,
			&b.CacheReadTokens, &b.CacheWriteTokens,
			&b.TotalCostUSD, &b.SessionCount,
		); err != nil {
			return nil, fmt.Errorf("scan bucket: %w", err)
		}
		b.TotalTokens = b.InputTokens + b.OutputTokens + b.CacheReadTokens + b.CacheWriteTokens
		out = append(out, b)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("rows iter: %w", err)
	}
	return out, nil
}

// Projects returns top-N projects ordered by total_cost_usd DESC.
func Projects(conn *sql.DB, limit int) ([]Project, error) {
	if limit <= 0 {
		limit = 20
	}
	if limit > 200 {
		limit = 200
	}
	const q = `
SELECT
  COALESCE(project_path,'(unknown)') AS project_path,
  COALESCE(SUM(input_tokens),0),
  COALESCE(SUM(output_tokens),0),
  COALESCE(SUM(cache_read_tokens),0),
  COALESCE(SUM(cache_write_tokens),0),
  COALESCE(SUM(total_cost_usd),0),
  COUNT(1)
FROM sessions
GROUP BY project_path
ORDER BY SUM(total_cost_usd) DESC
LIMIT ?
`
	rows, err := conn.Query(q, limit)
	if err != nil {
		return nil, fmt.Errorf("projects query: %w", err)
	}
	defer rows.Close()

	out := make([]Project, 0, 16)
	for rows.Next() {
		var p Project
		if err := rows.Scan(
			&p.ProjectPath, &p.InputTokens, &p.OutputTokens,
			&p.CacheReadTokens, &p.CacheWriteTokens,
			&p.TotalCostUSD, &p.SessionCount,
		); err != nil {
			return nil, fmt.Errorf("scan project: %w", err)
		}
		p.TotalTokens = p.InputTokens + p.OutputTokens + p.CacheReadTokens + p.CacheWriteTokens
		out = append(out, p)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("rows iter: %w", err)
	}
	return out, nil
}

// ModelRow is what ByModel scans from a session row before aggregation.
type ModelRow struct {
	EndedAt          string
	Model            string
	InputTokens      int64
	OutputTokens     int64
	CacheReadTokens  int64
	CacheWriteTokens int64
	TotalCostUSD     float64
}

// ByModel returns per-model totals plus a per-day breakdown for the last `days`
// days. Family is one of "opus" | "sonnet" | "haiku" | "other".
func ByModel(conn *sql.DB, days int) (ByModelResponse, error) {
	if days <= 0 {
		days = 14
	}
	if days > 365 {
		days = 365
	}
	since := time.Now().UTC().AddDate(0, 0, -days+1)
	cutoff := time.Date(since.Year(), since.Month(), since.Day(), 0, 0, 0, 0, time.UTC)
	const q = `
SELECT ended_at, COALESCE(model,''),
       input_tokens, output_tokens, cache_read_tokens, cache_write_tokens,
       total_cost_usd
FROM sessions
WHERE ended_at >= ?
`
	rows, err := conn.Query(q, cutoff.Format(time.RFC3339))
	if err != nil {
		return ByModelResponse{}, fmt.Errorf("by-model query: %w", err)
	}
	defer rows.Close()

	collected := make([]ModelRow, 0, 64)
	for rows.Next() {
		var r ModelRow
		if err := rows.Scan(
			&r.EndedAt, &r.Model,
			&r.InputTokens, &r.OutputTokens, &r.CacheReadTokens, &r.CacheWriteTokens,
			&r.TotalCostUSD,
		); err != nil {
			return ByModelResponse{}, fmt.Errorf("scan by-model: %w", err)
		}
		collected = append(collected, r)
	}
	if err := rows.Err(); err != nil {
		return ByModelResponse{}, fmt.Errorf("rows iter: %w", err)
	}
	return AggregateByModel(collected), nil
}

// AggregateByModel is the pure function that builds a ByModelResponse from a
// flat slice of rows. Exposed for testing and for the mock data path.
func AggregateByModel(rows []ModelRow) ByModelResponse {
	totals := map[string]*ModelTotal{}
	dailyMap := map[string]map[string]int64{} // bucket -> family -> tokens

	for _, r := range rows {
		fam := ModelFamily(r.Model)
		t, ok := totals[fam]
		if !ok {
			t = &ModelTotal{Family: fam}
			totals[fam] = t
		}
		t.InputTokens += r.InputTokens
		t.OutputTokens += r.OutputTokens
		t.CacheReadTokens += r.CacheReadTokens
		t.CacheWriteTokens += r.CacheWriteTokens
		t.TotalCostUSD += r.TotalCostUSD
		t.SessionCount++
		tokens := r.InputTokens + r.OutputTokens + r.CacheReadTokens + r.CacheWriteTokens
		t.TotalTokens += tokens

		day := r.EndedAt
		if len(day) >= 10 {
			day = day[:10]
		}
		if _, ok := dailyMap[day]; !ok {
			dailyMap[day] = map[string]int64{}
		}
		dailyMap[day][fam] += tokens
	}

	// Deterministic ordering for both slices.
	totalsOut := make([]ModelTotal, 0, len(totals))
	for _, v := range totals {
		totalsOut = append(totalsOut, *v)
	}
	sort.Slice(totalsOut, func(i, j int) bool {
		return totalsOut[i].TotalCostUSD > totalsOut[j].TotalCostUSD
	})

	days := make([]string, 0, len(dailyMap))
	for d := range dailyMap {
		days = append(days, d)
	}
	sort.Strings(days)
	dailyOut := make([]DailyByModel, 0, len(days))
	for _, d := range days {
		dailyOut = append(dailyOut, DailyByModel{Bucket: d, Totals: dailyMap[d]})
	}
	return ByModelResponse{Totals: totalsOut, Daily: dailyOut}
}

// ModelFamily classifies a model string into a coarse family bucket.
func ModelFamily(model string) string {
	m := strings.ToLower(model)
	switch {
	case strings.Contains(m, "opus"):
		return "opus"
	case strings.Contains(m, "haiku"):
		return "haiku"
	case strings.Contains(m, "sonnet"):
		return "sonnet"
	case m == "":
		return "other"
	default:
		return "other"
	}
}
