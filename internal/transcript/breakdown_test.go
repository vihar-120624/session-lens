package transcript

import (
	"strings"
	"testing"
)

const fixtureJSONL = `{"type":"user","timestamp":"2026-05-27T10:00:00.000Z","message":{"content":"hi"}}
{"type":"assistant","timestamp":"2026-05-27T10:00:02.000Z","message":{"model":"claude-sonnet-4-6","content":[{"type":"text","text":"thinking"},{"type":"tool_use","name":"Read"}],"usage":{"input_tokens":100,"output_tokens":50,"cache_read_input_tokens":2000,"cache_creation_input_tokens":0}}}
{"type":"user","timestamp":"2026-05-27T10:00:05.000Z","message":{"content":"more"}}
{"type":"assistant","timestamp":"2026-05-27T10:00:08.000Z","message":{"model":"claude-opus-4-7","content":[{"type":"tool_use","name":"Bash"},{"type":"tool_use","name":"Read"}],"usage":{"input_tokens":200,"output_tokens":120,"cache_read_input_tokens":3000,"cache_creation_input_tokens":1000}}}
`

func TestBreakdown_TurnsToolsAndModelSwitch(t *testing.T) {
	bd, err := ParseBreakdown(strings.NewReader(fixtureJSONL))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if bd.TurnCount != 2 {
		t.Errorf("turn_count: want 2 got %d", bd.TurnCount)
	}
	if len(bd.Turns) != 2 {
		t.Fatalf("turns: want 2 got %d", len(bd.Turns))
	}

	// First turn: sonnet, one Read tool, no cache_write.
	if bd.Turns[0].Model != "claude-sonnet-4-6" {
		t.Errorf("turn0 model: %q", bd.Turns[0].Model)
	}
	if len(bd.Turns[0].Tools) != 1 || bd.Turns[0].Tools[0] != "Read" {
		t.Errorf("turn0 tools: %v", bd.Turns[0].Tools)
	}
	if bd.Turns[0].InputTokens != 100 || bd.Turns[0].OutputTokens != 50 {
		t.Errorf("turn0 tokens: in=%d out=%d", bd.Turns[0].InputTokens, bd.Turns[0].OutputTokens)
	}
	if bd.Turns[0].CostUSD <= 0 {
		t.Errorf("turn0 cost should be >0, got %v", bd.Turns[0].CostUSD)
	}

	// Second turn: opus with Bash + Read.
	if bd.Turns[1].Model != "claude-opus-4-7" {
		t.Errorf("turn1 model: %q", bd.Turns[1].Model)
	}
	if len(bd.Turns[1].Tools) != 2 {
		t.Errorf("turn1 tools count: %v", bd.Turns[1].Tools)
	}

	// Tool counts aggregated across turns.
	if bd.ToolCounts["Read"] != 2 {
		t.Errorf("Read count: want 2 got %d", bd.ToolCounts["Read"])
	}
	if bd.ToolCounts["Bash"] != 1 {
		t.Errorf("Bash count: want 1 got %d", bd.ToolCounts["Bash"])
	}

	// Model switch ordering by first-seen.
	if len(bd.ModelSwitch) != 2 || bd.ModelSwitch[0] != "claude-sonnet-4-6" || bd.ModelSwitch[1] != "claude-opus-4-7" {
		t.Errorf("model_switch: %v", bd.ModelSwitch)
	}

	// Duration: first user msg at 10:00:00, last assistant at 10:00:08 → 8s.
	if bd.DurationSecs < 7.9 || bd.DurationSecs > 8.1 {
		t.Errorf("duration: want ~8s got %v", bd.DurationSecs)
	}
}
