package translate

import (
	"strings"
	"testing"
)

func TestOpenAIToGemini(t *testing.T) {
	req := parse(t, `{
	  "messages":[
	    {"role":"system","content":"sys"},
	    {"role":"user","content":[{"type":"text","text":"look"},{"type":"image_url","image_url":{"url":"data:image/png;base64,AAA"}},{"type":"image_url","image_url":{"url":"https://x/y.png"}}]},
	    {"role":"assistant","content":null,"tool_calls":[{"id":"t1","type":"function","function":{"name":"w","arguments":"{\"city\":\"X\"}"}},{"id":"t2","type":"function","function":{"name":"v","arguments":"{}"}}]},
	    {"role":"tool","tool_call_id":"t1","content":"{\"temp\":3}"},
	    {"role":"tool","tool_call_id":"t2","content":"plain text"}
	  ],
	  "temperature":0.2,"top_p":0.9,"max_tokens":50,"stop":"END",
	  "tools":[{"type":"function","function":{"name":"w","description":"d","parameters":{"type":"object","$schema":"x","additionalProperties":false,"properties":{"city":{"type":["string","null"]}}}}}],
	  "tool_choice":{"type":"function","function":{"name":"w"}}}`)
	out := OpenAIToGemini(req, "gemini-x")

	if asMap(asSlice(asMap(out["systemInstruction"])["parts"])[0])["text"] != "sys" {
		t.Fatalf("system: %v", out["systemInstruction"])
	}
	cs := asSlice(out["contents"])
	if len(cs) != 3 {
		t.Fatalf("want 3 contents (user, model, merged tool responses), got %d: %v", len(cs), cs)
	}
	userParts := asSlice(asMap(cs[0])["parts"])
	if len(userParts) != 3 || asMap(userParts[1])["inlineData"] == nil || asMap(userParts[2])["fileData"] == nil {
		t.Fatalf("user parts: %v", userParts)
	}
	if asMap(cs[1])["role"] != "model" || asMap(asSlice(asMap(cs[1])["parts"])[0])["functionCall"] == nil {
		t.Fatalf("model: %v", cs[1])
	}
	fr := asSlice(asMap(cs[2])["parts"])
	if len(fr) != 2 {
		t.Fatalf("function responses not merged: %v", fr)
	}
	r1 := asMap(asMap(fr[0])["functionResponse"])
	r2 := asMap(asMap(fr[1])["functionResponse"])
	if r1["name"] != "w" || asMap(r1["response"])["temp"] == nil || r2["name"] != "v" || asMap(r2["response"])["result"] != "plain text" {
		t.Fatalf("function responses: %v %v", r1, r2)
	}
	gc := asMap(out["generationConfig"])
	if gc["temperature"] != 0.2 || gc["topP"] != 0.9 || gc["maxOutputTokens"] != 50 || len(asSlice(gc["stopSequences"])) != 1 {
		t.Fatalf("generationConfig: %v", gc)
	}
	decl := asMap(asSlice(asMap(asSlice(out["tools"])[0])["functionDeclarations"])[0])
	params := asMap(decl["parameters"])
	if _, bad := params["additionalProperties"]; bad || params["$schema"] != nil {
		t.Fatalf("schema not cleaned: %v", params)
	}
	city := asMap(asMap(params["properties"])["city"])
	if city["type"] != "string" || city["nullable"] != true {
		t.Fatalf("nullable type: %v", city)
	}
	fcc := asMap(asMap(out["toolConfig"])["functionCallingConfig"])
	if fcc["mode"] != "ANY" || asSlice(fcc["allowedFunctionNames"])[0] != "w" {
		t.Fatalf("toolConfig: %v", fcc)
	}
}

func TestGeminiRespToOpenAI(t *testing.T) {
	resp := parse(t, `{"candidates":[{"content":{"parts":[{"text":"think","thought":true},{"text":"hi"},{"functionCall":{"name":"f","args":{"a":1}}}]},"finishReason":"STOP"}],
	  "usageMetadata":{"promptTokenCount":4,"candidatesTokenCount":6},"modelVersion":"gm-1"}`)
	o := GeminiRespToOpenAI(resp, "gm")
	ch := asMap(asSlice(o["choices"])[0])
	msg := asMap(ch["message"])
	if msg["content"] != "hi" || ch["finish_reason"] != "tool_calls" || o["model"] != "gm-1" {
		t.Fatalf("got %v", o)
	}
	tc := asMap(asSlice(msg["tool_calls"])[0])
	if !strings.HasPrefix(str(tc["id"]), "call_") || asMap(tc["function"])["arguments"] != `{"a":1}` {
		t.Fatalf("tool call: %v", tc)
	}
	if in, out := Usage(o); in != 4 || out != 6 {
		t.Fatalf("usage %d %d", in, out)
	}

	for finish, want := range map[string]string{"MAX_TOKENS": "length", "SAFETY": "content_filter", "STOP": "stop"} {
		r := parse(t, `{"candidates":[{"content":{"parts":[{"text":"x"}]},"finishReason":"`+finish+`"}]}`)
		if got := asMap(asSlice(GeminiRespToOpenAI(r, "m")["choices"])[0])["finish_reason"]; got != want {
			t.Fatalf("%s -> %v want %s", finish, got, want)
		}
	}
}

func TestGeminiStreamToOpenAI(t *testing.T) {
	sse := strings.Join([]string{
		`data: {"candidates":[{"content":{"parts":[{"text":"a"}]}}],"responseId":"r1"}`,
		`data: {"candidates":[{"content":{"parts":[{"text":"b"},{"functionCall":{"name":"f","args":{"x":1}}}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":3,"candidatesTokenCount":2}}`,
	}, "\n\n") + "\n\n"
	var chunks []map[string]any
	if err := GeminiStreamToOpenAI(strings.NewReader(sse), "m", func(c map[string]any) error {
		chunks = append(chunks, c)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	// role, "a", "b", tool call, finish
	if len(chunks) != 5 {
		t.Fatalf("chunks=%d: %v", len(chunks), chunks)
	}
	delta := func(i int) map[string]any { return asMap(asMap(asSlice(chunks[i]["choices"])[0])["delta"]) }
	if delta(0)["role"] != "assistant" || delta(1)["content"] != "a" || delta(2)["content"] != "b" {
		t.Fatalf("deltas: %v", chunks)
	}
	tc := asMap(asSlice(delta(3)["tool_calls"])[0])
	if tc["index"] != 0 || asMap(tc["function"])["name"] != "f" {
		t.Fatalf("tool call chunk: %v", tc)
	}
	last := chunks[4]
	if asMap(asSlice(last["choices"])[0])["finish_reason"] != "tool_calls" {
		t.Fatalf("finish: %v", last)
	}
	if in, out := Usage(last); in != 3 || out != 2 {
		t.Fatalf("usage %d %d", in, out)
	}
	if chunks[0]["id"] != "r1" {
		t.Fatalf("id %v", chunks[0]["id"])
	}
}
