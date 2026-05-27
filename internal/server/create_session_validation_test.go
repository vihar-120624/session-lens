package server_test

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
)

// TestCreateSession_MissingID verifies that POSTing a session without the
// required "id" field returns 400 with an informative error body.
func TestCreateSession_MissingID(t *testing.T) {
	ts := newTestServer(t)
	defer ts.Close()

	body := `{"project_path":"/tmp/proj","total_cost_usd":0.01,"model":"claude-sonnet","turns":1}`
	resp, err := http.Post(ts.URL+"/v1/sessions", "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", resp.StatusCode)
	}
	var errBody map[string]string
	if err := json.NewDecoder(resp.Body).Decode(&errBody); err != nil {
		t.Fatalf("decode error body: %v", err)
	}
	msg, ok := errBody["error"]
	if !ok || msg == "" {
		t.Errorf("expected non-empty 'error' field in response, got %v", errBody)
	}
}

// TestCreateSession_MalformedJSON verifies that sending a malformed JSON body
// returns 400.
func TestCreateSession_MalformedJSON(t *testing.T) {
	ts := newTestServer(t)
	defer ts.Close()

	body := `{this is not valid json`
	resp, err := http.Post(ts.URL+"/v1/sessions", "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", resp.StatusCode)
	}
}

// TestCreateSession_UnknownField verifies that posting a body with an
// unrecognised JSON field returns 400. The server calls
// json.Decoder.DisallowUnknownFields() — this test pins that intentional
// strictness so it is never accidentally removed.
func TestCreateSession_UnknownField(t *testing.T) {
	ts := newTestServer(t)
	defer ts.Close()

	body := `{"id":"sess-unknown-field","totally_unknown_key":"value","model":"claude-sonnet","turns":1}`
	resp, err := http.Post(ts.URL+"/v1/sessions", "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	rawBody, _ := io.ReadAll(resp.Body)
	resp.Body.Close()

	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400 for unknown field (DisallowUnknownFields), got %d; body: %s", resp.StatusCode, rawBody)
	}
}

// TestCreateSession_NoContentTypeCheck documents that the server does NOT
// enforce a Content-Type header — it decodes JSON regardless of content type.
// If this behaviour is intentionally changed to require application/json, this
// test should be updated to expect 415.
func TestCreateSession_NoContentTypeCheck(t *testing.T) {
	ts := newTestServer(t)
	defer ts.Close()

	body := `{"id":"sess-no-ct","model":"claude-sonnet","turns":1}`
	// Send without Content-Type header.
	req, err := http.NewRequest(http.MethodPost, ts.URL+"/v1/sessions", strings.NewReader(body))
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	// Intentionally leave Content-Type unset.
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	resp.Body.Close()

	// The server currently accepts this; it does not enforce Content-Type.
	// 200 or 201 both indicate acceptance — 400 or 415 would signal a new check.
	if resp.StatusCode == http.StatusUnsupportedMediaType {
		t.Skip("server now enforces Content-Type: update this test to expect 415")
	}
	if resp.StatusCode != http.StatusCreated && resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200/201 when no Content-Type is sent (no enforcement), got %d", resp.StatusCode)
	}
}
