package stats

import (
	"database/sql"
	"fmt"
	"sort"
	"time"
)

// SpikeKind enumerates the two anomaly types surfaced to the dashboard.
type SpikeKind string

const (
	// SpikeSession means a single session vastly exceeded the median session
	// cost over the recent window.
	SpikeSession SpikeKind = "session"
	// SpikeTrend means a calendar day's total tokens exceeded 2x the rolling
	// 7-day median.
	SpikeTrend SpikeKind = "trend"
)

// Spike is one anomaly worth surfacing to the user.
type Spike struct {
	Kind      SpikeKind `json:"kind"`
	Timestamp string    `json:"timestamp"`
	Project   string    `json:"project,omitempty"`
	Value     float64   `json:"value"`
	Baseline  float64   `json:"baseline"`
	Ratio     float64   `json:"ratio"`
	Severity  string    `json:"severity"` // low | medium | high
}

// SpikeConfig tunes the detection thresholds.
type SpikeConfig struct {
	SessionWindowN int     // how many recent sessions form the baseline
	SessionRatio   float64 // multiplier above median to count as a spike
	TrendRatio     float64 // day vs 7-day median multiplier
}

// DefaultSpikeConfig is the sensible default surfaced in the spec.
func DefaultSpikeConfig() SpikeConfig {
	return SpikeConfig{
		SessionWindowN: 20,
		SessionRatio:   3.0,
		TrendRatio:     2.0,
	}
}

// SessionRecord is the minimal session view needed for spike scoring.
type SessionRecord struct {
	ID          string
	EndedAt     time.Time
	ProjectPath string
	CostUSD     float64
	Tokens      int64
}

// DayRecord is the rollup view for trend-deviation scoring.
type DayRecord struct {
	Date   string // YYYY-MM-DD
	Tokens int64
}

// DetectSpikes runs both detectors and returns the union sorted newest-first.
// `sessions` must be sorted oldest-first; `days` must also be oldest-first.
func DetectSpikes(sessions []SessionRecord, days []DayRecord, cfg SpikeConfig) []Spike {
	if cfg.SessionWindowN <= 0 {
		cfg.SessionWindowN = 20
	}
	if cfg.SessionRatio <= 0 {
		cfg.SessionRatio = 3.0
	}
	if cfg.TrendRatio <= 0 {
		cfg.TrendRatio = 2.0
	}

	out := make([]Spike, 0, 16)
	out = append(out, detectSessionSpikes(sessions, cfg)...)
	out = append(out, detectTrendSpikes(days, cfg)...)

	sort.Slice(out, func(i, j int) bool {
		return out[i].Timestamp > out[j].Timestamp
	})
	return out
}

func detectSessionSpikes(sessions []SessionRecord, cfg SpikeConfig) []Spike {
	if len(sessions) < 3 {
		return nil
	}
	out := make([]Spike, 0)
	for i, s := range sessions {
		// Baseline = median cost of the prior N sessions (or all earlier ones).
		start := i - cfg.SessionWindowN
		if start < 0 {
			start = 0
		}
		if start == i {
			continue
		}
		baseline := medianCost(sessions[start:i])
		if baseline <= 0 {
			continue
		}
		if s.CostUSD >= baseline*cfg.SessionRatio {
			ratio := s.CostUSD / baseline
			out = append(out, Spike{
				Kind:      SpikeSession,
				Timestamp: s.EndedAt.UTC().Format(time.RFC3339),
				Project:   s.ProjectPath,
				Value:     s.CostUSD,
				Baseline:  baseline,
				Ratio:     ratio,
				Severity:  severityFor(ratio, cfg.SessionRatio),
			})
		}
	}
	return out
}

func detectTrendSpikes(days []DayRecord, cfg SpikeConfig) []Spike {
	if len(days) < 4 {
		return nil
	}
	out := make([]Spike, 0)
	for i, d := range days {
		// Need at least three prior days to form a 7-day rolling baseline.
		start := i - 7
		if start < 0 {
			start = 0
		}
		if i-start < 3 {
			continue
		}
		window := days[start:i]
		baseline := medianTokens(window)
		if baseline <= 0 {
			continue
		}
		if float64(d.Tokens) >= baseline*cfg.TrendRatio {
			ratio := float64(d.Tokens) / baseline
			out = append(out, Spike{
				Kind:      SpikeTrend,
				Timestamp: d.Date,
				Value:     float64(d.Tokens),
				Baseline:  baseline,
				Ratio:     ratio,
				Severity:  severityFor(ratio, cfg.TrendRatio),
			})
		}
	}
	return out
}

func medianCost(rows []SessionRecord) float64 {
	if len(rows) == 0 {
		return 0
	}
	values := make([]float64, len(rows))
	for i, r := range rows {
		values[i] = r.CostUSD
	}
	return medianFloat(values)
}

func medianTokens(rows []DayRecord) float64 {
	if len(rows) == 0 {
		return 0
	}
	values := make([]float64, len(rows))
	for i, r := range rows {
		values[i] = float64(r.Tokens)
	}
	return medianFloat(values)
}

func medianFloat(values []float64) float64 {
	if len(values) == 0 {
		return 0
	}
	sorted := make([]float64, len(values))
	copy(sorted, values)
	sort.Float64s(sorted)
	n := len(sorted)
	if n%2 == 1 {
		return sorted[n/2]
	}
	return (sorted[n/2-1] + sorted[n/2]) / 2.0
}

func severityFor(ratio, threshold float64) string {
	switch {
	case ratio >= threshold*3:
		return "high"
	case ratio >= threshold*1.5:
		return "medium"
	default:
		return "low"
	}
}

// Spikes is the DB-backed convenience: pulls the recent sessions + daily
// totals, runs detection, and returns the result.
func Spikes(conn *sql.DB, cfg SpikeConfig) ([]Spike, error) {
	// 1) Pull recent sessions (90 days, ordered oldest-first).
	since := time.Now().UTC().AddDate(0, 0, -90)
	cutoff := time.Date(since.Year(), since.Month(), since.Day(), 0, 0, 0, 0, time.UTC)
	const sessQ = `
SELECT id, ended_at, COALESCE(project_path,''),
       total_cost_usd,
       input_tokens + output_tokens + cache_read_tokens + cache_write_tokens
FROM sessions
WHERE ended_at >= ?
ORDER BY ended_at ASC
`
	rows, err := conn.Query(sessQ, cutoff.Format(time.RFC3339))
	if err != nil {
		return nil, fmt.Errorf("spikes sessions: %w", err)
	}
	sessions := make([]SessionRecord, 0, 64)
	for rows.Next() {
		var (
			id, endedAt, project string
			cost                 float64
			tokens               int64
		)
		if err := rows.Scan(&id, &endedAt, &project, &cost, &tokens); err != nil {
			rows.Close()
			return nil, fmt.Errorf("scan spike session: %w", err)
		}
		t, err := time.Parse(time.RFC3339, endedAt)
		if err != nil {
			// Best-effort: try date-only.
			if t2, err2 := time.Parse("2006-01-02", endedAt); err2 == nil {
				t = t2
			}
		}
		sessions = append(sessions, SessionRecord{
			ID: id, EndedAt: t, ProjectPath: project, CostUSD: cost, Tokens: tokens,
		})
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("rows iter sessions: %w", err)
	}

	// 2) Pull daily totals (90 days, ordered oldest-first).
	const dayQ = `
SELECT substr(ended_at,1,10) AS day,
       COALESCE(SUM(input_tokens + output_tokens + cache_read_tokens + cache_write_tokens),0)
FROM sessions
WHERE ended_at >= ?
GROUP BY day
ORDER BY day ASC
`
	drows, err := conn.Query(dayQ, cutoff.Format(time.RFC3339))
	if err != nil {
		return nil, fmt.Errorf("spikes days: %w", err)
	}
	days := make([]DayRecord, 0, 90)
	for drows.Next() {
		var d DayRecord
		if err := drows.Scan(&d.Date, &d.Tokens); err != nil {
			drows.Close()
			return nil, fmt.Errorf("scan spike day: %w", err)
		}
		days = append(days, d)
	}
	drows.Close()
	if err := drows.Err(); err != nil {
		return nil, fmt.Errorf("rows iter days: %w", err)
	}

	return DetectSpikes(sessions, days, cfg), nil
}
