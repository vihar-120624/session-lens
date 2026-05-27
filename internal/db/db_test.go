package db

import (
	"testing"
	"time"
)

func TestBootstrapAndUpsert(t *testing.T) {
	conn, err := Open(":memory:")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer conn.Close()

	s := Session{
		ID:               "sess-1",
		ProjectPath:      "/tmp/proj",
		StartedAt:        "2026-05-27T10:00:00Z",
		EndedAt:          "2026-05-27T10:30:00Z",
		InputTokens:      100,
		OutputTokens:     50,
		CacheReadTokens:  10,
		CacheWriteTokens: 5,
		TotalCostUSD:     0.0123,
		Model:            "claude-sonnet-4-5",
		Turns:            3,
		RawPayload:       `{"session_id":"sess-1"}`,
	}

	got, inserted, err := UpsertSession(conn, s)
	if err != nil {
		t.Fatalf("UpsertSession insert: %v", err)
	}
	if !inserted {
		t.Errorf("expected inserted=true on first insert")
	}
	if got.ID != s.ID || got.Turns != 3 || got.InputTokens != 100 {
		t.Errorf("row mismatch after insert: %+v", got)
	}
	if got.CreatedAt == "" || got.UpdatedAt == "" {
		t.Errorf("created_at/updated_at not populated: %+v", got)
	}
	createdAt := got.CreatedAt
	firstUpdated := got.UpdatedAt

	// Ensure SQLite CURRENT_TIMESTAMP advances at least a second.
	time.Sleep(1100 * time.Millisecond)

	s.OutputTokens = 200
	s.Turns = 4
	got2, inserted2, err := UpsertSession(conn, s)
	if err != nil {
		t.Fatalf("UpsertSession update: %v", err)
	}
	if inserted2 {
		t.Errorf("expected inserted=false on update")
	}
	if got2.OutputTokens != 200 || got2.Turns != 4 {
		t.Errorf("row not updated: %+v", got2)
	}
	if got2.CreatedAt != createdAt {
		t.Errorf("created_at changed across update: %q -> %q", createdAt, got2.CreatedAt)
	}
	if got2.UpdatedAt == firstUpdated {
		t.Errorf("updated_at did not advance: %q", got2.UpdatedAt)
	}
}

func TestIndexesPresent(t *testing.T) {
	conn, err := Open(":memory:")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer conn.Close()

	rows, err := conn.Query(`SELECT name FROM sqlite_master WHERE type='index' AND tbl_name='sessions'`)
	if err != nil {
		t.Fatalf("query indexes: %v", err)
	}
	defer rows.Close()

	found := map[string]bool{}
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatalf("scan: %v", err)
		}
		found[name] = true
	}
	for _, want := range []string{"idx_sessions_ended_at", "idx_sessions_project"} {
		if !found[want] {
			t.Errorf("missing index %q (found: %v)", want, found)
		}
	}
}
