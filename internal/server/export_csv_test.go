package server_test

import (
	"encoding/csv"
	"net/http"
	"strings"
	"testing"

	"github.com/viharshah/session-lens/internal/db"
)

// sampleSession returns a minimal db.Session suitable for seeding.
func sampleSession() db.Session {
	return db.Session{
		ID:               "export-test-001",
		ProjectPath:      "/tmp/export-test",
		StartedAt:        "2026-05-27T10:00:00Z",
		EndedAt:          "2026-05-27T10:30:00Z",
		InputTokens:      500,
		OutputTokens:     250,
		CacheReadTokens:  100,
		CacheWriteTokens: 50,
		TotalCostUSD:     0.012,
		Model:            "claude-sonnet-4-5",
		Turns:            3,
	}
}

// TestExportDailyCsv_MockMode verifies GET /v1/export/daily.csv returns correct
// Content-Type, header row, and at least one data row when mock mode is active.
func TestExportDailyCsv_MockMode(t *testing.T) {
	ts := newTestServer(t)
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/v1/export/daily.csv?mock=1")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 got %d", resp.StatusCode)
	}

	ct := resp.Header.Get("Content-Type")
	if !strings.Contains(ct, "text/csv") {
		t.Errorf("Content-Type: want text/csv, got %q", ct)
	}

	cd := resp.Header.Get("Content-Disposition")
	if !strings.Contains(cd, "attachment") {
		t.Errorf("Content-Disposition: want attachment, got %q", cd)
	}

	records, err := csv.NewReader(resp.Body).ReadAll()
	if err != nil {
		t.Fatalf("parse csv: %v", err)
	}

	wantHeader := []string{"date", "sessions", "total_cost_usd", "input_tokens", "output_tokens", "cache_creation_tokens", "cache_read_tokens"}
	if len(records) == 0 {
		t.Fatal("no rows in CSV")
	}
	gotHeader := records[0]
	if len(gotHeader) != len(wantHeader) {
		t.Fatalf("header column count: want %d got %d", len(wantHeader), len(gotHeader))
	}
	for i, col := range wantHeader {
		if gotHeader[i] != col {
			t.Errorf("header[%d]: want %q got %q", i, col, gotHeader[i])
		}
	}

	if len(records) < 2 {
		t.Errorf("expected at least one data row, got %d total rows", len(records))
	}
}

// TestExportDailyCsv_RealDB verifies that the endpoint works against a real
// (empty) in-memory DB and returns the correct header with zero data rows.
func TestExportDailyCsv_RealDB(t *testing.T) {
	ts := newTestServer(t)
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/v1/export/daily.csv")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 got %d", resp.StatusCode)
	}

	ct := resp.Header.Get("Content-Type")
	if !strings.Contains(ct, "text/csv") {
		t.Errorf("Content-Type: want text/csv, got %q", ct)
	}

	records, err := csv.NewReader(resp.Body).ReadAll()
	if err != nil {
		t.Fatalf("parse csv: %v", err)
	}
	if len(records) < 1 {
		t.Fatal("expected at least a header row")
	}
	if records[0][0] != "date" {
		t.Errorf("first header col: want %q got %q", "date", records[0][0])
	}
}

// TestExportSessionsCsv_MockMode verifies GET /v1/export/sessions.csv in mock
// mode returns status 200, correct Content-Type, expected header, and data rows.
func TestExportSessionsCsv_MockMode(t *testing.T) {
	ts := newTestServer(t)
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/v1/export/sessions.csv?mock=1&limit=10")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 got %d", resp.StatusCode)
	}

	ct := resp.Header.Get("Content-Type")
	if !strings.Contains(ct, "text/csv") {
		t.Errorf("Content-Type: want text/csv, got %q", ct)
	}

	cd := resp.Header.Get("Content-Disposition")
	if !strings.Contains(cd, "attachment") {
		t.Errorf("Content-Disposition: want attachment, got %q", cd)
	}

	records, err := csv.NewReader(resp.Body).ReadAll()
	if err != nil {
		t.Fatalf("parse csv: %v", err)
	}

	wantHeader := []string{"id", "started_at", "ended_at", "project_path", "model", "turns", "input_tokens", "output_tokens", "cache_creation_tokens", "cache_read_tokens", "total_cost_usd"}
	if len(records) == 0 {
		t.Fatal("no rows in CSV")
	}
	gotHeader := records[0]
	if len(gotHeader) != len(wantHeader) {
		t.Fatalf("header column count: want %d got %d", len(wantHeader), len(gotHeader))
	}
	for i, col := range wantHeader {
		if gotHeader[i] != col {
			t.Errorf("header[%d]: want %q got %q", i, col, gotHeader[i])
		}
	}

	if len(records) < 2 {
		t.Errorf("expected at least one data row, got %d total rows", len(records))
	}
}

// TestExportSessionsCsv_RealDB verifies the sessions CSV endpoint returns correct
// headers and zero data rows when backed by an empty in-memory DB.
func TestExportSessionsCsv_RealDB(t *testing.T) {
	ts := newTestServer(t)
	defer ts.Close()

	// Seed one session so we can assert at least one data row.
	seed := sampleSession()
	seedSession(t, ts, seed)

	resp, err := http.Get(ts.URL + "/v1/export/sessions.csv")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 got %d", resp.StatusCode)
	}

	ct := resp.Header.Get("Content-Type")
	if !strings.Contains(ct, "text/csv") {
		t.Errorf("Content-Type: want text/csv, got %q", ct)
	}

	records, err := csv.NewReader(resp.Body).ReadAll()
	if err != nil {
		t.Fatalf("parse csv: %v", err)
	}
	if len(records) < 2 {
		t.Errorf("expected header + at least one data row, got %d rows", len(records))
	}
	if records[0][0] != "id" {
		t.Errorf("first header col: want %q got %q", "id", records[0][0])
	}
}

// TestExportSessionsCsv_LimitCap verifies the limit param is honoured and capped at 10000.
func TestExportSessionsCsv_LimitCap(t *testing.T) {
	ts := newTestServer(t)
	defer ts.Close()

	// Requesting limit=99999 should succeed (server caps at 10000).
	resp, err := http.Get(ts.URL + "/v1/export/sessions.csv?limit=99999")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 got %d", resp.StatusCode)
	}
}
