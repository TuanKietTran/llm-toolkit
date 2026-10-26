package tokensaver

import (
	"strings"
	"testing"

	"gorouter/internal/config"
)

func TestOptimizationControls(t *testing.T) {
	body := map[string]any{"messages": []any{
		map[string]any{"role": "system", "content": "rules"},
		map[string]any{"role": "tool", "content": "same"},
		map[string]any{"role": "tool", "content": "same"},
		map[string]any{"role": "user", "content": strings.Repeat("old", 30)},
		map[string]any{"role": "assistant", "content": "new"},
	}}
	x := ApplyDetailed(body, config.TokenSaver{DedupeToolMessages: true, MaxHistoryMessages: 2, MaxPromptChars: 180, CacheControl: true})
	if x.Dedupe == 0 || x.History == 0 {
		t.Fatalf("savings = %+v", x)
	}
	msgs := body["messages"].([]any)
	if len(msgs) > 3 {
		t.Fatalf("history not bounded: %d", len(msgs))
	}
	sys := msgs[0].(map[string]any)
	if sys["cache_control"] == nil {
		t.Fatal("cache control missing")
	}
}
