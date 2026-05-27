package server_test

import (
	"bufio"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/viharshah/session-lens/internal/db"
	"github.com/viharshah/session-lens/internal/server"
)

// newTestServerWithHub returns a test server wired to the given hub so tests
// can call hub.Broadcast directly and observe SSE output.
func newTestServerWithHub(t *testing.T, hub *server.Hub) *httptest.Server {
	t.Helper()
	conn, err := db.Open(":memory:")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { conn.Close() })
	h := server.New(server.Config{
		DB:          conn,
		MockDefault: false,
		Hub:         hub,
	})
	return httptest.NewServer(h)
}

// TestSSE_ReceivesSessionEvent connects to /v1/events, broadcasts a session
// via the hub, and asserts the SSE frame arrives within 1 second.
func TestSSE_ReceivesSessionEvent(t *testing.T) {
	hub := server.NewHub()
	ts := newTestServerWithHub(t, hub)
	defer ts.Close()

	// Open the SSE stream.  We need to read asynchronously.
	req, err := http.NewRequest(http.MethodGet, ts.URL+"/v1/events", nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	resp, err := ts.Client().Do(req)
	if err != nil {
		t.Fatalf("GET /v1/events: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	ct := resp.Header.Get("Content-Type")
	if !strings.HasPrefix(ct, "text/event-stream") {
		t.Fatalf("expected text/event-stream, got %q", ct)
	}

	// Read SSE frames in a goroutine; send matching events on the channel.
	type frame struct{ event, data string }
	frames := make(chan frame, 4)
	go func() {
		scanner := bufio.NewScanner(resp.Body)
		var ev, data string
		for scanner.Scan() {
			line := scanner.Text()
			switch {
			case strings.HasPrefix(line, "event:"):
				ev = strings.TrimSpace(strings.TrimPrefix(line, "event:"))
			case strings.HasPrefix(line, "data:"):
				data = strings.TrimSpace(strings.TrimPrefix(line, "data:"))
			case line == "":
				if ev != "" || data != "" {
					frames <- frame{ev, data}
					ev, data = "", ""
				}
			}
		}
	}()

	// Give the SSE handler time to subscribe before we broadcast.
	time.Sleep(20 * time.Millisecond)

	want := db.Session{
		ID:           "sse-test-001",
		ProjectPath:  "/projects/test",
		StartedAt:    "2026-05-27T10:00:00Z",
		EndedAt:      "2026-05-27T10:05:00Z",
		InputTokens:  100,
		OutputTokens: 50,
		TotalCostUSD: 0.001,
		Model:        "claude-sonnet-4-6",
		Turns:        3,
	}

	hub.Broadcast(want)

	// Wait for the frame (up to 1 second).
	select {
	case f := <-frames:
		if f.event != "session" {
			t.Fatalf("expected event=session, got %q", f.event)
		}
		var got db.Session
		if err := json.Unmarshal([]byte(f.data), &got); err != nil {
			t.Fatalf("unmarshal data: %v", err)
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
		if got.Model != want.Model {
			t.Errorf("model: want %q got %q", want.Model, got.Model)
		}
	case <-time.After(1 * time.Second):
		t.Fatal("timed out waiting for SSE frame within 1s")
	}
}
