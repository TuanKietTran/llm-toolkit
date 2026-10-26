package translate

import "testing"

func TestAnthropicCacheControlSurvivesPivot(t *testing.T) {
	in := map[string]any{"model": "m", "system": []any{map[string]any{"type": "text", "text": "rules", "cache_control": map[string]any{"type": "ephemeral"}}}, "messages": []any{map[string]any{"role": "user", "content": []any{map[string]any{"type": "text", "text": "hi", "cache_control": map[string]any{"type": "ephemeral"}}}}}}
	out := AnthropicToOpenAI(in)
	msgs := out["messages"].([]any)
	if msgs[0].(map[string]any)["cache_control"] == nil {
		t.Fatal("system cache control lost")
	}
	parts := msgs[1].(map[string]any)["content"].([]any)
	if parts[0].(map[string]any)["cache_control"] == nil {
		t.Fatal("content cache control lost")
	}
}

func TestOpenAIToAnthropicCarriesCacheControl(t *testing.T) {
	cc := map[string]any{"type": "ephemeral"}
	req := map[string]any{"messages": []any{
		map[string]any{"role": "system", "content": "rules", "cache_control": cc},
		map[string]any{"role": "user", "content": []any{map[string]any{"type": "text", "text": "hi", "cache_control": cc}}},
	}, "tools": []any{map[string]any{"type": "function", "cache_control": cc, "function": map[string]any{"name": "f"}}}}
	out := OpenAIToAnthropic(req, "m")
	sys, ok := out["system"].([]any)
	if !ok || sys[0].(map[string]any)["cache_control"] == nil {
		t.Fatalf("system should be marked blocks, got %#v", out["system"])
	}
	user := out["messages"].([]any)[0].(map[string]any)["content"].([]any)[0].(map[string]any)
	if user["cache_control"] == nil {
		t.Fatal("user text block lost cache_control")
	}
	if out["tools"].([]any)[0].(map[string]any)["cache_control"] == nil {
		t.Fatal("tool lost cache_control")
	}
	plain := OpenAIToAnthropic(map[string]any{"messages": []any{map[string]any{"role": "system", "content": "rules"}}}, "m")
	if _, isString := plain["system"].(string); !isString {
		t.Fatalf("unmarked system must stay a string, got %#v", plain["system"])
	}
}
