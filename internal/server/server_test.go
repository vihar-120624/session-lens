package server_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/viharshah/session-lens/internal/db"
	"github.com/viharshah/session-lens/internal/server"
)

// newTestServer opens an in-memory DB, bootstraps the schema, and returns a
// test server backed by real DB (mock mode off).
func newTestServer(t *testing.T) *httptest.Server {
	t.Helper()
	conn, err := db.Open(":memory:")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { conn.Close() })
	h := server.New(server.Config{
		DB:          conn,
		MockDefault: false,
	})
	return httptest.NewServer(h)
}

// seedEvent is the subset of fields accepted by POST /v1/sessions.
type seedEvent struct {
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
}

// seedSession inserts one session into the server's DB via the POST endpoint.
func seedSession(t *testing.T, ts *httptest.Server, s db.Session) {
	t.Helper()
	ev := seedEvent{
		ID:               s.ID,
		ProjectPath:      s.ProjectPath,
		StartedAt:        s.StartedAt,
		EndedAt:          s.EndedAt,
		InputTokens:      s.InputTokens,
		OutputTokens:     s.OutputTokens,
		CacheReadTokens:  s.CacheReadTokens,
		CacheWriteTokens: s.CacheWriteTokens,
		TotalCostUSD:     s.TotalCostUSD,
		Model:            s.Model,
		Turns:            s.Turns,
	}
	body, _ := json.Marshal(ev)
	resp, err := http.Post(ts.URL+"/v1/sessions", "application/json", strings.NewReader(string(body)))
	if err != nil {
		t.Fatalf("seed POST: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusCreated && resp.StatusCode != http.StatusOK {
		t.Fatalf("seed POST returned %d", resp.StatusCode)
	}
}

// TestGetSession_200 verifies that a seeded session is retrievable via
// GET /v1/sessions/{id} and that all expected fields are present.
func TestGetSession_200(t *testing.T) {
	ts := newTestServer(t)
	defer ts.Close()

	want := db.Session{
		ID:               "test-sess-abc",
		ProjectPath:      "/tmp/myproject",
		StartedAt:        "2026-05-27T09:00:00Z",
		EndedAt:          "2026-05-27T09:30:00Z",
		InputTokens:      1234,
		OutputTokens:     567,
		CacheReadTokens:  890,
		CacheWriteTokens: 123,
		TotalCostUSD:     0.0456,
		Model:            "claude-sonnet-4-5",
		Turns:            5,
	}
	seedSession(t, ts, want)

	resp, err := http.Get(ts.URL + "/v1/sessions/test-sess-abc")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 got %d", resp.StatusCode)
	}

	var got db.Session
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Fatalf("decode: %v", err)
	}

	if got.ID != want.ID {
		t.Errorf("id: want %q got %q", want.ID, got.ID)
	}
	if got.ProjectPath != want.ProjectPath {
		t.Errorf("project_path: want %q got %q", want.ProjectPath, got.ProjectPath)
	}
	if got.InputTokens != want.InputTokens {
		t.Errorf("input_tokens: want %d got %d", want.InputTokens, got.InputTokens)
	}
	if got.OutputTokens != want.OutputTokens {
		t.Errorf("output_tokens: want %d got %d", want.OutputTokens, got.OutputTokens)
	}
	if got.CacheReadTokens != want.CacheReadTokens {
		t.Errorf("cache_read_tokens: want %d got %d", want.CacheReadTokens, got.CacheReadTokens)
	}
	if got.CacheWriteTokens != want.CacheWriteTokens {
		t.Errorf("cache_write_tokens: want %d got %d", want.CacheWriteTokens, got.CacheWriteTokens)
	}
	if got.TotalCostUSD != want.TotalCostUSD {
		t.Errorf("total_cost_usd: want %v got %v", want.TotalCostUSD, got.TotalCostUSD)
	}
	if got.Model != want.Model {
		t.Errorf("model: want %q got %q", want.Model, got.Model)
	}
	if got.Turns != want.Turns {
		t.Errorf("turns: want %d got %d", want.Turns, got.Turns)
	}
	if got.CreatedAt == "" {
		t.Error("created_at is empty")
	}
	if got.UpdatedAt == "" {
		t.Error("updated_at is empty")
	}
}

// TestListSessions_DateRange verifies that ?from=&to= bounds are respected.
func TestListSessions_DateRange(t *testing.T) {
	ts := newTestServer(t)
	defer ts.Close()

	// Three sessions across two days.
	sessions := []db.Session{
		{ID: "s-may26", EndedAt: "2026-05-26T23:00:00Z", Model: "claude-sonnet-4-6", TotalCostUSD: 0.10},
		{ID: "s-may27a", EndedAt: "2026-05-27T09:30:00Z", Model: "claude-sonnet-4-6", TotalCostUSD: 0.20},
		{ID: "s-may27b", EndedAt: "2026-05-27T18:45:00Z", Model: "claude-sonnet-4-6", TotalCostUSD: 0.30},
		{ID: "s-may28", EndedAt: "2026-05-28T01:00:00Z", Model: "claude-sonnet-4-6", TotalCostUSD: 0.40},
	}
	for _, s := range sessions {
		seedSession(t, ts, s)
	}

	// Range covers all of May 27 UTC only.
	resp, err := http.Get(ts.URL + "/v1/sessions?from=2026-05-27T00:00:00Z&to=2026-05-27T23:59:59Z&limit=20")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 got %d", resp.StatusCode)
	}

	var got []db.Session
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("expected 2 sessions in May 27 range, got %d", len(got))
	}
	for _, s := range got {
		if s.ID != "s-may27a" && s.ID != "s-may27b" {
			t.Errorf("unexpected session id in range: %s", s.ID)
		}
	}
}

// TestGetSession_404 verifies that requesting an unknown session id returns 404.
func TestGetSession_404(t *testing.T) {
	ts := newTestServer(t)
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/v1/sessions/does-not-exist")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	resp.Body.Close()

	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("expected 404 got %d", resp.StatusCode)
	}
}

// TestMethodNotAllowed_405 verifies that sending the wrong HTTP method to a
// route that is registered for a specific method returns 405 Method Not Allowed.
// This pins the Go 1.22 ServeMux method-routing behaviour.
func TestMethodNotAllowed_405(t *testing.T) {
	ts := newTestServer(t)
	defer ts.Close()

	// GET /v1/sessions/{id} is the only registered method for this path pattern.
	// POSTing to it must return 405 (not 404).
	req, err := http.NewRequest(http.MethodPost, ts.URL+"/v1/sessions/some-id", nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	resp.Body.Close()

	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("expected 405 Method Not Allowed for POST /v1/sessions/{id}, got %d", resp.StatusCode)
	}
}

// TestGetSession_MockMode verifies that mock sessions surface via the new
// endpoint when mock mode is enabled (mock=1 query param).
func TestGetSession_MockMode(t *testing.T) {
	ts := newTestServer(t)
	defer ts.Close()

	// First list sessions in mock mode to get a real id.
	resp, err := http.Get(ts.URL + "/v1/sessions?mock=1&limit=5")
	if err != nil {
		t.Fatalf("list GET: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("list expected 200 got %d", resp.StatusCode)
	}

	var sessions []db.Session
	if err := json.NewDecoder(resp.Body).Decode(&sessions); err != nil {
		t.Fatalf("decode list: %v", err)
	}
	if len(sessions) == 0 {
		t.Fatal("expected at least one mock session")
	}

	id := sessions[0].ID

	resp2, err := http.Get(ts.URL + "/v1/sessions/" + id + "?mock=1")
	if err != nil {
		t.Fatalf("GET by id: %v", err)
	}
	defer resp2.Body.Close()

	if resp2.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 for mock session got %d", resp2.StatusCode)
	}

	var got db.Session
	if err := json.NewDecoder(resp2.Body).Decode(&got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.ID != id {
		t.Errorf("id mismatch: want %q got %q", id, got.ID)
	}
}
