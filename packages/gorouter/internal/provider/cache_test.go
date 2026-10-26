package provider

import (
	"context"
	"io"
	"strings"
	"testing"

	"gorouter/internal/config"
)

func body() map[string]any {
	cc := map[string]any{"type": "ephemeral"}
	return map[string]any{"messages": []any{
		map[string]any{"role": "system", "content": "rules", "cache_control": cc},
		map[string]any{"role": "user", "content": []any{map[string]any{"type": "text", "text": "hi", "cache_control": cc}}},
	}}
}

func build(t *testing.T, typ, base string) string {
	t.Helper()
	p := New(config.Provider{Type: typ, BaseURL: base})
	req, err := p.BuildRequest(context.Background(), body(), "m", "")
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(req.Body)
	return string(b)
}

func TestCacheControlPerUpstream(t *testing.T) {
	if got := build(t, KindAnthropic, "https://api.anthropic.com"); strings.Count(got, "cache_control") != 2 {
		t.Fatalf("anthropic must keep both markers: %s", got)
	}
	if got := build(t, KindOpenAI, "https://api.openai.com/v1"); strings.Contains(got, "cache_control") {
		t.Fatalf("api.openai.com must not receive markers: %s", got)
	}
	if got := build(t, KindOpenAI, "https://openrouter.ai/api/v1"); strings.Count(got, "cache_control") != 2 {
		t.Fatalf("openrouter keeps markers: %s", got)
	}
}
