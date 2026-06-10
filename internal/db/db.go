// Package db owns the SQLite connection, schema bootstrap, and the
// SessionEvent CRUD used by the API layer.
package db

import (
	"database/sql"
	"fmt"
	"time"

	_ "modernc.org/sqlite"
)

const schema = `
PRAGMA journal_mode=WAL;
CREATE TABLE IF NOT EXISTS sessions (
  id TEXT PRIMARY KEY,
  project_path TEXT,
  started_at TEXT,
  ended_at TEXT NOT NULL,
  input_tokens INTEGER NOT NULL DEFAULT 0,
  output_tokens INTEGER NOT NULL DEFAULT 0,
  cache_read_tokens INTEGER NOT NULL DEFAULT 0,
  cache_write_tokens INTEGER NOT NULL DEFAULT 0,
  total_cost_usd REAL NOT NULL DEFAULT 0,
  model TEXT,
  turns INTEGER NOT NULL DEFAULT 0,
  raw_payload TEXT,
  created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX IF NOT EXISTS idx_sessions_ended_at ON sessions(ended_at);
CREATE INDEX IF NOT EXISTS idx_sessions_project ON sessions(project_path);
CREATE INDEX IF NOT EXISTS idx_sessions_project_ended_at ON sessions(project_path, ended_at);
CREATE INDEX IF NOT EXISTS idx_sessions_model_ended_at ON sessions(model, ended_at);
CREATE INDEX IF NOT EXISTS idx_sessions_created_at ON sessions(created_at);
`

// Session is the persisted row plus identity.
type Session struct {
	ID               string  `json:"id"`
	ProjectPath      string  `json:"project_path"`
	StartedAt        string  `json:"started_at"`
	EndedAt          string  `json:"ended_at"`
	InputTokens      int64   `json:"input_tokens"`
	OutputTokens     int64   `json:"output_tokens"`
	CacheReadTokens  int64   `json:"cache_read_tokens"`
	CacheWriteTokens int64   `json:"cache_write_tokens"`
	TotalCostUSD     float64 `json:"total_cost_usd"`
	Model            string  `json:"model"`
	Turns            int     `json:"turns"`
	RawPayload       string  `json:"raw_payload,omitempty"`
	CreatedAt        string  `json:"created_at"`
	UpdatedAt        string  `json:"updated_at"`
}

// Open returns a connection pool to the given path (or ":memory:") with the
// schema applied. Caller owns Close().
func Open(path string) (*sql.DB, error) {
	conn, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}
	conn.SetMaxOpenConns(8)
	conn.SetMaxIdleConns(4)
	conn.SetConnMaxLifetime(30 * time.Minute)

	if err := Bootstrap(conn); err != nil {
		conn.Close()
		return nil, err
	}
	return conn, nil
}

// Bootstrap runs the schema DDL. Safe to call repeatedly.
func Bootstrap(conn *sql.DB) error {
	if _, err := conn.Exec(schema); err != nil {
		return fmt.Errorf("apply schema: %w", err)
	}
	return nil
}

// UpsertSession inserts or updates by primary key (session id). Returns
// (session, inserted) where inserted is true on a fresh insert, false on
// update.
func UpsertSession(conn *sql.DB, s Session) (Session, bool, error) {
	// Detect existence first so we can report inserted vs updated.
	var exists int
	err := conn.QueryRow(`SELECT COUNT(1) FROM sessions WHERE id = ?`, s.ID).Scan(&exists)
	if err != nil {
		return Session{}, false, fmt.Errorf("check exists: %w", err)
	}

	const stmt = `
INSERT INTO sessions (
  id, project_path, started_at, ended_at,
  input_tokens, output_tokens, cache_read_tokens, cache_write_tokens,
  total_cost_usd, model, turns, raw_payload
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(id) DO UPDATE SET
  project_path = excluded.project_path,
  started_at = excluded.started_at,
  ended_at = excluded.ended_at,
  input_tokens = excluded.input_tokens,
  output_tokens = excluded.output_tokens,
  cache_read_tokens = excluded.cache_read_tokens,
  cache_write_tokens = excluded.cache_write_tokens,
  total_cost_usd = excluded.total_cost_usd,
  model = excluded.model,
  turns = excluded.turns,
  raw_payload = excluded.raw_payload,
  updated_at = CURRENT_TIMESTAMP
`
	if _, err := conn.Exec(stmt,
		s.ID, s.ProjectPath, s.StartedAt, s.EndedAt,
		s.InputTokens, s.OutputTokens, s.CacheReadTokens, s.CacheWriteTokens,
		s.TotalCostUSD, s.Model, s.Turns, s.RawPayload,
	); err != nil {
		return Session{}, false, fmt.Errorf("upsert: %w", err)
	}

	out, err := GetSession(conn, s.ID)
	if err != nil {
		return Session{}, false, err
	}
	return out, exists == 0, nil
}

// ListSessions returns the most-recent sessions ordered by ended_at DESC.
// limit is capped at 100; pass 0 to get the default of 20.
func ListSessions(conn *sql.DB, limit int) ([]Session, error) {
	return ListSessionsFiltered(conn, limit, "", "")
}

// ListSessionsFiltered is ListSessions plus optional ended_at range filter.
// from/to are inclusive RFC3339 strings (UTC); pass "" to omit either bound.
// Lexicographic comparison is correct because all timestamps are stored
// `Z`-suffixed by the hook.
func ListSessionsFiltered(conn *sql.DB, limit int, from, to string) ([]Session, error) {
	if limit <= 0 {
		limit = 20
	}
	if limit > 10000 {
		limit = 10000
	}
	stmt := `
SELECT id, COALESCE(project_path,''), COALESCE(started_at,''), ended_at,
       input_tokens, output_tokens, cache_read_tokens, cache_write_tokens,
       total_cost_usd, COALESCE(model,''), turns, COALESCE(raw_payload,''),
       created_at, updated_at
FROM sessions WHERE 1=1`
	args := []any{}
	if from != "" {
		stmt += ` AND ended_at >= ?`
		args = append(args, from)
	}
	if to != "" {
		stmt += ` AND ended_at <= ?`
		args = append(args, to)
	}
	stmt += ` ORDER BY ended_at DESC LIMIT ?`
	args = append(args, limit)

	rows, err := conn.Query(stmt, args...)
	if err != nil {
		return nil, fmt.Errorf("list sessions: %w", err)
	}
	defer rows.Close()
	var out []Session
	for rows.Next() {
		var s Session
		if err := rows.Scan(
			&s.ID, &s.ProjectPath, &s.StartedAt, &s.EndedAt,
			&s.InputTokens, &s.OutputTokens, &s.CacheReadTokens, &s.CacheWriteTokens,
			&s.TotalCostUSD, &s.Model, &s.Turns, &s.RawPayload,
			&s.CreatedAt, &s.UpdatedAt,
		); err != nil {
			return nil, fmt.Errorf("scan session: %w", err)
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// GetSession fetches a single row by id.
func GetSession(conn *sql.DB, id string) (Session, error) {
	const stmt = `
SELECT id, COALESCE(project_path,''), COALESCE(started_at,''), ended_at,
       input_tokens, output_tokens, cache_read_tokens, cache_write_tokens,
       total_cost_usd, COALESCE(model,''), turns, COALESCE(raw_payload,''),
       created_at, updated_at
FROM sessions WHERE id = ?`
	var s Session
	err := conn.QueryRow(stmt, id).Scan(
		&s.ID, &s.ProjectPath, &s.StartedAt, &s.EndedAt,
		&s.InputTokens, &s.OutputTokens, &s.CacheReadTokens, &s.CacheWriteTokens,
		&s.TotalCostUSD, &s.Model, &s.Turns, &s.RawPayload,
		&s.CreatedAt, &s.UpdatedAt,
	)
	if err != nil {
		return Session{}, fmt.Errorf("get session: %w", err)
	}
	return s, nil
}
