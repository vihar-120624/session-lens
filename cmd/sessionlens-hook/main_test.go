package main

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// --------------------------------------------------------------------------
// Fake HTTP client helpers
// --------------------------------------------------------------------------

// roundTripFunc lets tests supply a function as an http.RoundTripper.
type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// fakeClient builds an httpPoster backed by the given RoundTripper.
func fakeClient(rt http.RoundTripper) httpPoster {
	return &http.Client{Transport: rt}
}

// response500 returns a minimal 500 response.
func response(code int) *http.Response {
	return &http.Response{
		StatusCode: code,
		Body:       io.NopCloser(bytes.NewReader(nil)),
	}
}

// --------------------------------------------------------------------------
// postWithRetry tests
// --------------------------------------------------------------------------

// TestRetryRecovery: first two calls return 500, third returns 200 → success.
func TestRetryRecovery(t *testing.T) {
	// Override back-off to zero for speed.
	orig := retryBackoff
	retryBackoff = []time.Duration{0, 0, 0}
	defer func() { retryBackoff = orig }()

	calls := 0
	client := fakeClient(roundTripFunc(func(r *http.Request) (*http.Response, error) {
		calls++
		if calls < 3 {
			return response(500), nil
		}
		return response(200), nil
	}))

	body := []byte(`{"id":"test"}`)
	if err := postWithRetry(client, "http://example.com", body); err != nil {
		t.Fatalf("expected success after recovery, got: %v", err)
	}
	if calls != 3 {
		t.Errorf("expected 3 calls, got %d", calls)
	}
}

// TestRetryExhausted: all attempts return 500 → error returned (caller buffers).
func TestRetryExhausted(t *testing.T) {
	orig := retryBackoff
	retryBackoff = []time.Duration{0, 0, 0}
	defer func() { retryBackoff = orig }()

	calls := 0
	client := fakeClient(roundTripFunc(func(r *http.Request) (*http.Response, error) {
		calls++
		return response(500), nil
	}))

	body := []byte(`{"id":"test"}`)
	err := postWithRetry(client, "http://example.com", body)
	if err == nil {
		t.Fatal("expected error when all retries exhausted")
	}
	// 4 total attempts: 1 initial + 3 back-off slots
	if calls != 4 {
		t.Errorf("expected 4 attempts, got %d", calls)
	}
}

// TestNonRetryable4xx: a 400 response is not retried.
func TestNonRetryable4xx(t *testing.T) {
	orig := retryBackoff
	retryBackoff = []time.Duration{0, 0, 0}
	defer func() { retryBackoff = orig }()

	calls := 0
	client := fakeClient(roundTripFunc(func(r *http.Request) (*http.Response, error) {
		calls++
		return response(400), nil
	}))

	err := postWithRetry(client, "http://example.com", []byte(`{}`))
	if err == nil {
		t.Fatal("expected error for 4xx")
	}
	if calls != 1 {
		t.Errorf("expected exactly 1 call for 4xx, got %d", calls)
	}
}

// --------------------------------------------------------------------------
// Buffer write tests
// --------------------------------------------------------------------------

// TestPermanentFailureBuffersEvent: all retries fail → a file appears in buffer.
func TestPermanentFailureBuffersEvent(t *testing.T) {
	orig := retryBackoff
	retryBackoff = []time.Duration{0, 0, 0}
	defer func() { retryBackoff = orig }()

	dir := t.TempDir()

	client := fakeClient(roundTripFunc(func(r *http.Request) (*http.Response, error) {
		return nil, fmt.Errorf("connection refused")
	}))

	payload := []byte(`{"id":"buf-test","project_path":"/tmp/x"}`)
	if err := postWithRetry(client, "http://127.0.0.1:9999", payload); err == nil {
		t.Fatal("expected error")
	}
	if err := writeBuffer(dir, payload); err != nil {
		t.Fatalf("writeBuffer: %v", err)
	}

	files, _ := filepath.Glob(filepath.Join(dir, "*.json"))
	if len(files) != 1 {
		t.Fatalf("expected 1 buffer file, got %d", len(files))
	}
	data, _ := os.ReadFile(files[0])
	if string(data) != string(payload) {
		t.Errorf("buffer content mismatch: got %q, want %q", data, payload)
	}
}

// TestBufferCap: writing bufferCap+5 files should not exceed bufferCap.
func TestBufferCap(t *testing.T) {
	dir := t.TempDir()
	payload := []byte(`{"id":"cap-test"}`)

	for i := 0; i < bufferCap+5; i++ {
		time.Sleep(time.Millisecond) // ensure distinct nanosecond timestamps
		if err := writeBuffer(dir, payload); err != nil {
			t.Fatalf("writeBuffer %d: %v", i, err)
		}
	}

	files, _ := filepath.Glob(filepath.Join(dir, "*.json"))
	if len(files) > bufferCap {
		t.Errorf("buffer cap exceeded: %d files (cap %d)", len(files), bufferCap)
	}
}

// --------------------------------------------------------------------------
// Drain buffer tests
// --------------------------------------------------------------------------

// TestDrainEmptiesBufferOnSuccess: server returns 200 → buffer files deleted.
func TestDrainEmptiesBufferOnSuccess(t *testing.T) {
	dir := t.TempDir()
	payload := []byte(`{"id":"drain-test"}`)

	// Write 3 files directly.
	for i := 0; i < 3; i++ {
		name := fmt.Sprintf("%013d-%06d.json", time.Now().UnixNano()+int64(i), i)
		if err := os.WriteFile(filepath.Join(dir, name), payload, 0o644); err != nil {
			t.Fatalf("seed: %v", err)
		}
	}

	client := fakeClient(roundTripFunc(func(r *http.Request) (*http.Response, error) {
		return response(200), nil
	}))

	drainBuffer(client, "http://example.com", dir)

	files, _ := filepath.Glob(filepath.Join(dir, "*.json"))
	if len(files) != 0 {
		t.Errorf("expected buffer empty after drain, got %d files", len(files))
	}
}

// TestDrainLeavesFilesOnFailure: server still failing → files remain.
func TestDrainLeavesFilesOnFailure(t *testing.T) {
	orig := retryBackoff
	retryBackoff = []time.Duration{0, 0, 0}
	defer func() { retryBackoff = orig }()

	dir := t.TempDir()
	payload := []byte(`{"id":"drain-fail"}`)

	name := fmt.Sprintf("%013d-000000.json", time.Now().UnixNano())
	if err := os.WriteFile(filepath.Join(dir, name), payload, 0o644); err != nil {
		t.Fatalf("seed: %v", err)
	}

	client := fakeClient(roundTripFunc(func(r *http.Request) (*http.Response, error) {
		return response(500), nil
	}))

	drainBuffer(client, "http://example.com", dir)

	files, _ := filepath.Glob(filepath.Join(dir, "*.json"))
	if len(files) != 1 {
		t.Errorf("expected 1 file to remain, got %d", len(files))
	}
}

// TestDrainThenPost: drain succeeds first, then current event posts — simulates full happy path.
func TestDrainThenPost(t *testing.T) {
	orig := retryBackoff
	retryBackoff = []time.Duration{0, 0, 0}
	defer func() { retryBackoff = orig }()

	dir := t.TempDir()
	payload := []byte(`{"id":"old-event"}`)
	name := fmt.Sprintf("%013d-000000.json", time.Now().UnixNano())
	if err := os.WriteFile(filepath.Join(dir, name), payload, 0o644); err != nil {
		t.Fatalf("seed: %v", err)
	}

	posted := 0
	client := fakeClient(roundTripFunc(func(r *http.Request) (*http.Response, error) {
		posted++
		return response(200), nil
	}))

	// Drain the buffered event.
	drainBuffer(client, "http://example.com", dir)
	// Post a new event.
	if err := postWithRetry(client, "http://example.com", []byte(`{"id":"new"}`)); err != nil {
		t.Fatalf("postWithRetry: %v", err)
	}

	if posted != 2 {
		t.Errorf("expected 2 total POSTs (1 drain + 1 new), got %d", posted)
	}
	files, _ := filepath.Glob(filepath.Join(dir, "*.json"))
	if len(files) != 0 {
		t.Errorf("buffer should be empty after successful drain")
	}
}
