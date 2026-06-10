package transcript

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"sort"
	"time"
)

// Turn is a per-assistant-message slice of the transcript: token usage, model,
// and the names of any tool_use blocks emitted in that turn.
type Turn struct {
	Index            int      `json:"index"`
	Timestamp        string   `json:"timestamp"`
	Model            string   `json:"model"`
	InputTokens      int64    `json:"input_tokens"`
	OutputTokens     int64    `json:"output_tokens"`
	CacheReadTokens  int64    `json:"cache_read_tokens"`
	CacheWriteTokens int64    `json:"cache_write_tokens"`
	CostUSD          float64  `json:"cost_usd"`
	Tools            []string `json:"tools"`
}

// Breakdown is the per-turn detail returned by GET /v1/sessions/{id}/breakdown.
type Breakdown struct {
	Turns        []Turn         `json:"turns"`
	ToolCounts   map[string]int `json:"tool_counts"`
	ModelSwitch  []string       `json:"model_switch"` // distinct models in order seen
	TurnCount    int            `json:"turn_count"`
	DurationSecs float64        `json:"duration_secs"`
}

// turnHeader captures the top-level type+timestamp without imposing any shape
// on `message` — user lines have message.content as a string, assistant lines
// have it as an array, so we decode message in a second pass.
type turnHeader struct {
	Type      string          `json:"type"`
	Timestamp string          `json:"timestamp"`
	Message   json.RawMessage `json:"message"`
}

// assistantMessage is the subset of an assistant message used for breakdown.
type assistantMessage struct {
	Model   string `json:"model"`
	Content []struct {
		Type string `json:"type"`
		Name string `json:"name"`
	} `json:"content"`
	Usage *struct {
		InputTokens              int64 `json:"input_tokens"`
		OutputTokens             int64 `json:"output_tokens"`
		CacheReadInputTokens     int64 `json:"cache_read_input_tokens"`
		CacheCreationInputTokens int64 `json:"cache_creation_input_tokens"`
	} `json:"usage"`
}

// BreakdownFile parses a transcript JSONL file into per-turn detail.
func BreakdownFile(path string) (*Breakdown, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open transcript: %w", err)
	}
	defer f.Close()
	return ParseBreakdown(f)
}

// ParseBreakdown reads JSONL from r and builds per-turn detail. Malformed lines
// are skipped.
func ParseBreakdown(r io.Reader) (*Breakdown, error) {
	out := &Breakdown{
		ToolCounts: map[string]int{},
	}
	scanner := bufio.NewScanner(r)
	const maxLine = 64 * 1024 * 1024
	scanner.Buffer(make([]byte, 0, 64*1024), maxLine)

	var firstTS, lastTS time.Time
	seenModels := map[string]int{} // model -> first-seen turn index

	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		var hdr turnHeader
		if err := json.Unmarshal(line, &hdr); err != nil {
			continue
		}
		if hdr.Timestamp != "" {
			if ts, err := time.Parse(time.RFC3339Nano, hdr.Timestamp); err == nil {
				if firstTS.IsZero() || ts.Before(firstTS) {
					firstTS = ts
				}
				if ts.After(lastTS) {
					lastTS = ts
				}
			}
		}
		if hdr.Type != "assistant" || len(hdr.Message) == 0 {
			continue
		}
		var am assistantMessage
		if err := json.Unmarshal(hdr.Message, &am); err != nil {
			continue
		}

		t := Turn{
			Index:     len(out.Turns),
			Timestamp: hdr.Timestamp,
			Model:     am.Model,
		}
		if am.Usage != nil {
			t.InputTokens = clampTokens(am.Usage.InputTokens)
			t.OutputTokens = clampTokens(am.Usage.OutputTokens)
			t.CacheReadTokens = clampTokens(am.Usage.CacheReadInputTokens)
			t.CacheWriteTokens = clampTokens(am.Usage.CacheCreationInputTokens)
			t.CostUSD = ComputeCost(t.Model,
				t.InputTokens, t.OutputTokens,
				t.CacheReadTokens, t.CacheWriteTokens)
		}
		for _, c := range am.Content {
			if c.Type == "tool_use" && c.Name != "" {
				t.Tools = append(t.Tools, c.Name)
				out.ToolCounts[c.Name]++
			}
		}
		if t.Model != "" {
			if _, ok := seenModels[t.Model]; !ok {
				seenModels[t.Model] = t.Index
				out.ModelSwitch = append(out.ModelSwitch, t.Model)
			}
		}
		out.Turns = append(out.Turns, t)
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("scan transcript: %w", err)
	}

	out.TurnCount = len(out.Turns)
	if !firstTS.IsZero() && !lastTS.IsZero() {
		out.DurationSecs = lastTS.Sub(firstTS).Seconds()
	}
	// Stable model_switch ordering by first-seen index.
	sort.SliceStable(out.ModelSwitch, func(i, j int) bool {
		return seenModels[out.ModelSwitch[i]] < seenModels[out.ModelSwitch[j]]
	})
	return out, nil
}
