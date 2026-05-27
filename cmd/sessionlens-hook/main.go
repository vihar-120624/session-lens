// sessionlens-hook is the Claude Code Stop-hook binary. It reads the hook
// JSON event from stdin, parses the referenced transcript JSONL, computes
// token usage + cost, and POSTs the result to the local sessionlens-server.
//
// Operational contract: this binary MUST NOT block or fail Claude Code.
// Every error path is swallowed, the process always exits 0, and a panic
// recover guards main.
package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"time"

	"github.com/viharshah/session-lens/internal/transcript"
)

// hookInput is the subset of the Claude Code Stop-hook event we read.
type hookInput struct {
	SessionID      string `json:"session_id"`
	TranscriptPath string `json:"transcript_path"`
	CWD            string `json:"cwd"`
	HookEventName  string `json:"hook_event_name"`
}

// sessionEvent is the payload POSTed to the server.
type sessionEvent struct {
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
}

const (
	serverURLEnv  = "SESSION_LENS_URL"
	defaultServer = "http://127.0.0.1:7821/v1/sessions"
	hookLogPath   = "/tmp/session-lens-hook.log"
	hookTimeout   = 1500 * time.Millisecond
)

func main() {
	// Belt-and-suspenders: a panic anywhere below must not propagate to Claude.
	defer func() {
		if r := recover(); r != nil {
			logErr(fmt.Errorf("panic: %v", r))
		}
		os.Exit(0)
	}()

	run()
}

func run() {
	raw, err := io.ReadAll(os.Stdin)
	if err != nil {
		logErr(fmt.Errorf("read stdin: %w", err))
		return
	}
	if len(bytes.TrimSpace(raw)) == 0 {
		return
	}

	var in hookInput
	if err := json.Unmarshal(raw, &in); err != nil {
		logErr(fmt.Errorf("parse hook json: %w", err))
		return
	}
	if in.TranscriptPath == "" || in.SessionID == "" {
		logErr(fmt.Errorf("hook missing transcript_path or session_id"))
		return
	}

	summary, err := transcript.ParseFile(in.TranscriptPath)
	if err != nil {
		logErr(fmt.Errorf("parse transcript: %w", err))
		return
	}

	endedAt := summary.EndedAt
	if endedAt.IsZero() {
		endedAt = time.Now().UTC()
	}
	startedAt := summary.StartedAt
	startedISO := ""
	if !startedAt.IsZero() {
		startedISO = startedAt.UTC().Format(time.RFC3339)
	}

	ev := sessionEvent{
		ID:               in.SessionID,
		ProjectPath:      in.CWD,
		StartedAt:        startedISO,
		EndedAt:          endedAt.UTC().Format(time.RFC3339),
		InputTokens:      summary.InputTokens,
		OutputTokens:     summary.OutputTokens,
		CacheReadTokens:  summary.CacheReadTokens,
		CacheWriteTokens: summary.CacheWriteTokens,
		TotalCostUSD:     summary.TotalCostUSD,
		Model:            summary.Model,
		Turns:            summary.Turns,
		RawPayload:       string(raw),
	}

	body, err := json.Marshal(ev)
	if err != nil {
		logErr(fmt.Errorf("encode event: %w", err))
		return
	}

	url := os.Getenv(serverURLEnv)
	if url == "" {
		url = defaultServer
	}

	client := &http.Client{Timeout: hookTimeout}
	req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		logErr(fmt.Errorf("build request: %w", err))
		return
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "sessionlens-hook/0.1")

	resp, err := client.Do(req)
	if err != nil {
		logErr(fmt.Errorf("post: %w", err))
		return
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)
	if resp.StatusCode >= 400 {
		logErr(fmt.Errorf("server returned " + strconv.Itoa(resp.StatusCode)))
	}
}

// logErr appends an error line to the hook log. Failures here are silent.
func logErr(err error) {
	if err == nil {
		return
	}
	f, openErr := os.OpenFile(hookLogPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if openErr != nil {
		return
	}
	defer f.Close()
	fmt.Fprintf(f, "%s %v\n", time.Now().UTC().Format(time.RFC3339), err)
}
