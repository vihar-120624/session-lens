// Package stats provides read-only aggregate queries over the sessions table:
// current-month summary, daily/weekly buckets, and top-project rollups.
package stats

import (
	"database/sql"
	"fmt"
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
