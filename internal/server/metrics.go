package server

import (
	"database/sql"
	"os"
	"path/filepath"
	"sync/atomic"
	"time"
)

// metrics is the in-process counter set surfaced by GET /v1/metrics.
// Counters are bumped from the request log middleware and the ingest handler;
// reads via Snapshot are lock-free (load-only atomics).
type metrics struct {
	startedAt        time.Time
	requestCount     atomic.Int64
	ingestAccepted   atomic.Int64
	ingestRateLimted atomic.Int64
	ingestFailed     atomic.Int64
}

func newMetrics() *metrics {
	return &metrics{startedAt: time.Now()}
}

// MetricsSnapshot is the JSON shape returned by /v1/metrics.
type MetricsSnapshot struct {
	UptimeSecs        float64 `json:"uptime_secs"`
	StartedAt         string  `json:"started_at"`
	RequestCount      int64   `json:"request_count"`
	IngestAccepted    int64   `json:"ingest_accepted"`
	IngestRateLimited int64   `json:"ingest_rate_limited"`
	IngestFailed      int64   `json:"ingest_failed"`
	SessionCount      int64   `json:"session_count"`
	LastIngestAt      string  `json:"last_ingest_at"`
	DBSizeBytes       int64   `json:"db_size_bytes"`
	BufferFileCount   int     `json:"buffer_file_count"`
	Version           string  `json:"version"`
}

// Snapshot reads in-process counters and joins them with DB/file state.
// All errors are swallowed (the endpoint is best-effort observability).
func (m *metrics) Snapshot(conn *sql.DB, dbPath, bufferDir string) MetricsSnapshot {
	out := MetricsSnapshot{
		UptimeSecs:        time.Since(m.startedAt).Seconds(),
		StartedAt:         m.startedAt.UTC().Format(time.RFC3339),
		RequestCount:      m.requestCount.Load(),
		IngestAccepted:    m.ingestAccepted.Load(),
		IngestRateLimited: m.ingestRateLimted.Load(),
		IngestFailed:      m.ingestFailed.Load(),
		Version:           Version,
	}
	if conn != nil {
		var n int64
		_ = conn.QueryRow(`SELECT COUNT(1) FROM sessions`).Scan(&n)
		out.SessionCount = n
		var last sql.NullString
		_ = conn.QueryRow(`SELECT MAX(ended_at) FROM sessions`).Scan(&last)
		if last.Valid {
			out.LastIngestAt = last.String
		}
	}
	if dbPath != "" && dbPath != ":memory:" {
		if fi, err := os.Stat(dbPath); err == nil {
			out.DBSizeBytes = fi.Size()
		}
	}
	if bufferDir != "" {
		if files, err := filepath.Glob(filepath.Join(bufferDir, "*.json")); err == nil {
			out.BufferFileCount = len(files)
		}
	}
	return out
}
