package translate

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func parse(t *testing.T, s string) map[string]any {
	var m map[string]any
	if err := json.Unmarshal([]byte(s), &m); err != nil {
		t.Fatal(err)
	}
	return m
}

func TestOpenAIToAnthropicToolRoundTrip(t *testing.T) {
	req := parse(t, `{
	  "messages":[
	    {"role":"system","content":"sys"},
	    {"role":"user","content":"weather?"},
	    {"role":"assistant","content":null,"tool_calls":[{"id":"t1","type":"function","function":{"name":"w","arguments":"{\"city\":\"X\"}"}}]},
	    {"role":"tool","tool_call_id":"t1","content":"sunny"}
	  ],
	  "tools":[{"type":"function","function":{"name":"w","description":"d","parameters":{"type":"object"}}}],
	  "tool_choice":"required","stop":"END"}`)
	out := OpenAIToAnthropic(req, "claude")
	if out["system"] != "sys" || out["max_tokens"] != defaultMaxTokens {
		t.Fatalf("bad header fields: %v", out)
	}
	if !reflect.DeepEqual(out["stop_sequences"], []any{"END"}) {
		t.Fatalf("stop: %v", out["stop_sequences"])
	}
	if asMap(out["tool_choice"])["type"] != "any" {
		t.Fatalf("tool_choice: %v", out["tool_choice"])
	}
	msgs := asSlice(out["messages"])
	if len(msgs) != 3 {
		t.Fatalf("want 3 messages, got %d: %v", len(msgs), msgs)
	}
	tu := asMap(asSlice(asMap(msgs[1])["content"])[0])
	if tu["type"] != "tool_use" || asMap(tu["input"])["city"] != "X" {
		t.Fatalf("tool_use: %v", tu)
	}
	tr := asMap(asSlice(asMap(msgs[2])["content"])[0])
	if tr["type"] != "tool_result" || tr["tool_use_id"] != "t1" {
		t.Fatalf("tool_result: %v", tr)
	}

	// And back.
	back := AnthropicToOpenAI(out)
	bm := asSlice(back["messages"])
	if len(bm) != 4 || asMap(bm[3])["role"] != "tool" || len(asSlice(asMap(bm[2])["tool_calls"])) != 1 {
		t.Fatalf("round trip: %v", bm)
	}
	if back["tool_choice"] != "required" {
		t.Fatalf("tool_choice back: %v", back["tool_choice"])
	}
}

func TestImageDataURL(t *testing.T) {
	req := parse(t, `{"messages":[{"role":"user","content":[{"type":"image_url","image_url":{"url":"data:image/png;base64,AAA"}}]}]}`)
	out := OpenAIToAnthropic(req, "m")
	src := asMap(asMap(asSlice(asMap(asSlice(out["messages"])[0])["content"])[0])["source"])
	if src["media_type"] != "image/png" || src["data"] != "AAA" {
		t.Fatalf("source: %v", src)
	}
}

func TestResponseConversion(t *testing.T) {
	a := parse(t, `{"id":"i","model":"m","content":[{"type":"text","text":"hi"},{"type":"tool_use","id":"t","name":"f","input":{"a":1}}],"stop_reason":"tool_use","usage":{"input_tokens":2,"output_tokens":3}}`)
	o := AnthropicRespToOpenAI(a)
	ch := asMap(asSlice(o["choices"])[0])
	if ch["finish_reason"] != "tool_calls" {
		t.Fatalf("finish: %v", ch["finish_reason"])
	}
	if in, out := Usage(o); in != 2 || out != 3 {
		t.Fatalf("usage %d %d", in, out)
	}
	back := OpenAIRespToAnthropic(o)
	if back["stop_reason"] != "tool_use" || len(asSlice(back["content"])) != 2 {
		t.Fatalf("back: %v", back)
	}
}

func TestStreamAnthropicToOpenAIToAnthropic(t *testing.T) {
	sse := strings.Join([]string{
		`data: {"type":"message_start","message":{"id":"m1","model":"c","usage":{"input_tokens":4}}}`,
		`data: {"type":"content_block_start","index":0,"content_block":{"type":"text"}}`,
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"ab"}}`,
		`data: {"type":"content_block_start","index":1,"content_block":{"type":"tool_use","id":"t","name":"f"}}`,
		`data: {"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"{\"x\":1}"}}`,
		`data: {"type":"message_delta","delta":{"stop_reason":"tool_use"},"usage":{"output_tokens":7}}`,
	}, "\n\n") + "\n\n"

	var sb strings.Builder
	aw := NewAnthropicWriter(&sb, func() {})
	var chunks int
	err := AnthropicStreamToOpenAI(strings.NewReader(sse), func(c map[string]any) error {
		chunks++
		return aw.Write(c)
	})
	if err != nil {
		t.Fatal(err)
	}
	aw.Close()
	out := sb.String()
	for _, want := range []string{`"text_delta"`, `"tool_use"`, `input_json_delta`, `"stop_reason":"tool_use"`, `"output_tokens":7`, "message_stop"} {
		if !strings.Contains(out, want) {
			t.Fatalf("missing %s in:\n%s", want, out)
		}
	}
	if chunks != 5 {
		t.Fatalf("chunks=%d", chunks)
	}
}
