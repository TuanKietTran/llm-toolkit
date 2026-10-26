package translate

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"io"
	"strings"
	"time"
)

var geminiFinish = map[string]string{
	"STOP":       "stop",
	"MAX_TOKENS": "length",
}

// schema keys the Gemini API rejects.
var geminiDroppedSchemaKeys = map[string]bool{
	"additionalProperties": true, "$schema": true, "$id": true, "$ref": true,
	"definitions": true, "$defs": true, "strict": true,
}

func newCallID() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return "call_" + hex.EncodeToString(b)
}

// OpenAIToGemini converts an OpenAI chat request into a Gemini generateContent request.
// The model is carried in the URL, not the body.
func OpenAIToGemini(req map[string]any, _ string) map[string]any {
	out := map[string]any{}
	var system []string
	var contents []any
	toolNames := map[string]string{} // tool_call id -> function name

	appendContent := func(role string, parts []any) {
		if len(parts) == 0 {
			return
		}
		if n := len(contents); n > 0 {
			last := contents[n-1].(map[string]any)
			if last["role"] == role {
				last["parts"] = append(last["parts"].([]any), parts...)
				return
			}
		}
		contents = append(contents, map[string]any{"role": role, "parts": parts})
	}

	for _, m := range asSlice(req["messages"]) {
		mm := asMap(m)
		switch str(mm["role"]) {
		case "system", "developer":
			if t := contentText(mm["content"]); t != "" {
				system = append(system, t)
			}
		case "user":
			appendContent("user", geminiUserParts(mm["content"]))
		case "assistant":
			var parts []any
			if t := contentText(mm["content"]); t != "" {
				parts = append(parts, map[string]any{"text": t})
			}
			for _, tc := range asSlice(mm["tool_calls"]) {
				tcm := asMap(tc)
				fn := asMap(tcm["function"])
				name := str(fn["name"])
				toolNames[str(tcm["id"])] = name
				args, _ := parseJSONObject(str(fn["arguments"])).(map[string]any)
				if args == nil {
					args = map[string]any{}
				}
				parts = append(parts, map[string]any{"functionCall": map[string]any{"name": name, "args": args}})
			}
			appendContent("model", parts)
		case "tool":
			name := toolNames[str(mm["tool_call_id"])]
			if name == "" {
				name = str(mm["name"])
			}
			text := contentText(mm["content"])
			resp, ok := parseJSONObject(text).(map[string]any)
			if !ok || len(resp) == 0 {
				resp = map[string]any{"result": text}
			}
			appendContent("user", []any{map[string]any{"functionResponse": map[string]any{"name": name, "response": resp}}})
		}
	}
	out["contents"] = contents
	if len(system) > 0 {
		out["systemInstruction"] = map[string]any{"parts": []any{map[string]any{"text": strings.Join(system, "\n\n")}}}
	}

	gc := map[string]any{}
	if v, ok := req["temperature"]; ok {
		gc["temperature"] = v
	}
	if v, ok := req["top_p"]; ok {
		gc["topP"] = v
	}
	maxTokens := num(req["max_tokens"])
	if maxTokens == 0 {
		maxTokens = num(req["max_completion_tokens"])
	}
	if maxTokens > 0 {
		gc["maxOutputTokens"] = maxTokens
	}
	switch stop := req["stop"].(type) {
	case string:
		gc["stopSequences"] = []any{stop}
	case []any:
		gc["stopSequences"] = stop
	}
	if len(gc) > 0 {
		out["generationConfig"] = gc
	}

	if tools := asSlice(req["tools"]); len(tools) > 0 {
		var decls []any
		for _, t := range tools {
			fn := asMap(asMap(t)["function"])
			d := map[string]any{"name": str(fn["name"]), "description": str(fn["description"])}
			if params, ok := fn["parameters"].(map[string]any); ok && len(asMap(params["properties"])) > 0 {
				d["parameters"] = cleanGeminiSchema(params)
			}
			decls = append(decls, d)
		}
		out["tools"] = []any{map[string]any{"functionDeclarations": decls}}
	}
	switch tc := req["tool_choice"].(type) {
	case string:
		mode := map[string]string{"auto": "AUTO", "required": "ANY", "none": "NONE"}[tc]
		if mode != "" {
			out["toolConfig"] = map[string]any{"functionCallingConfig": map[string]any{"mode": mode}}
		}
	case map[string]any:
		if name := str(asMap(tc["function"])["name"]); name != "" {
			out["toolConfig"] = map[string]any{"functionCallingConfig": map[string]any{
				"mode": "ANY", "allowedFunctionNames": []any{name},
			}}
		}
	}
	return out
}

func geminiUserParts(content any) []any {
	if s, ok := content.(string); ok {
		if s == "" {
			return nil
		}
		return []any{map[string]any{"text": s}}
	}
	var parts []any
	for _, p := range asSlice(content) {
		pm := asMap(p)
		switch str(pm["type"]) {
		case "text":
			if t := str(pm["text"]); t != "" {
				parts = append(parts, map[string]any{"text": t})
			}
		case "image_url":
			url := str(pm["image_url"])
			if url == "" {
				url = str(asMap(pm["image_url"])["url"])
			}
			if rest, ok := strings.CutPrefix(url, "data:"); ok {
				if meta, data, found := strings.Cut(rest, ","); found {
					parts = append(parts, map[string]any{"inlineData": map[string]any{
						"mimeType": strings.TrimSuffix(meta, ";base64"), "data": data,
					}})
				}
			} else if url != "" {
				parts = append(parts, map[string]any{"fileData": map[string]any{"fileUri": url, "mimeType": "image/*"}})
			}
		}
	}
	return parts
}

// cleanGeminiSchema strips JSON-schema features Gemini does not accept, recursively.
func cleanGeminiSchema(v any) any {
	switch t := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(t))
		for k, val := range t {
			if geminiDroppedSchemaKeys[k] {
				continue
			}
			if k == "type" {
				// ["string","null"] -> "string" + nullable
				if arr, ok := val.([]any); ok {
					for _, e := range arr {
						if s := str(e); s == "null" {
							out["nullable"] = true
						} else if _, set := out["type"]; !set {
							out["type"] = s
						}
					}
					continue
				}
			}
			out[k] = cleanGeminiSchema(val)
		}
		return out
	case []any:
		out := make([]any, len(t))
		for i, e := range t {
			out[i] = cleanGeminiSchema(e)
		}
		return out
	}
	return v
}

type geminiParsed struct {
	text      string
	calls     []map[string]any // name, args
	finish    string
	in, out   int
	hasFinish bool
}

// parseGeminiCandidate merges the first candidate of a GenerateContentResponse into p.
func (p *geminiParsed) merge(resp map[string]any) {
	if cands := asSlice(resp["candidates"]); len(cands) > 0 {
		c := asMap(cands[0])
		for _, part := range asSlice(asMap(c["content"])["parts"]) {
			pm := asMap(part)
			if th, _ := pm["thought"].(bool); th {
				continue
			}
			if fc := asMap(pm["functionCall"]); fc != nil {
				p.calls = append(p.calls, fc)
				continue
			}
			p.text += str(pm["text"])
		}
		if f := str(c["finishReason"]); f != "" {
			p.finish, p.hasFinish = f, true
		}
	}
	if u := asMap(resp["usageMetadata"]); u != nil {
		p.in, p.out = num(u["promptTokenCount"]), num(u["candidatesTokenCount"])
	}
}

func (p *geminiParsed) finishReason(sawCalls bool) string {
	if sawCalls {
		return "tool_calls"
	}
	if f, ok := geminiFinish[p.finish]; ok {
		return f
	}
	switch p.finish {
	case "SAFETY", "RECITATION", "BLOCKLIST", "PROHIBITED_CONTENT", "SPII":
		return "content_filter"
	}
	return "stop"
}

func openAIToolCall(index int, id string, fc map[string]any) map[string]any {
	args := fc["args"]
	if args == nil {
		args = map[string]any{}
	}
	tc := map[string]any{
		"id": id, "type": "function",
		"function": map[string]any{"name": str(fc["name"]), "arguments": toJSON(args)},
	}
	if index >= 0 {
		tc["index"] = index
	}
	return tc
}

// GeminiRespToOpenAI converts a non-streaming GenerateContentResponse into a chat.completion.
func GeminiRespToOpenAI(resp map[string]any, model string) map[string]any {
	var p geminiParsed
	p.merge(resp)
	msg := map[string]any{"role": "assistant", "content": p.text}
	if len(p.calls) > 0 {
		var tcs []any
		for _, fc := range p.calls {
			tcs = append(tcs, openAIToolCall(-1, newCallID(), fc))
		}
		msg["tool_calls"] = tcs
		if p.text == "" {
			msg["content"] = nil
		}
	}
	id := str(resp["responseId"])
	if id == "" {
		id = "gemini-" + newCallID()[5:]
	}
	if mv := str(resp["modelVersion"]); mv != "" {
		model = mv
	}
	return map[string]any{
		"id": id, "object": "chat.completion", "created": time.Now().Unix(), "model": model,
		"choices": []any{map[string]any{"index": 0, "message": msg, "finish_reason": p.finishReason(len(p.calls) > 0)}},
		"usage":   map[string]any{"prompt_tokens": p.in, "completion_tokens": p.out, "total_tokens": p.in + p.out},
	}
}

// GeminiStreamToOpenAI reads a Gemini SSE stream (alt=sse) and emits OpenAI chunks.
func GeminiStreamToOpenAI(r io.Reader, model string, emit func(map[string]any) error) error {
	id := "gemini-" + newCallID()[5:]
	started, calls := false, 0
	var last geminiParsed
	err := ReadSSE(r, func(_, data string) error {
		var ev map[string]any
		if err := json.Unmarshal([]byte(data), &ev); err != nil {
			return nil
		}
		if rid := str(ev["responseId"]); rid != "" && !started {
			id = rid
		}
		if mv := str(ev["modelVersion"]); mv != "" {
			model = mv
		}
		if !started {
			started = true
			if err := emit(chunk(id, model, map[string]any{"role": "assistant", "content": ""}, nil)); err != nil {
				return err
			}
		}
		var p geminiParsed
		p.merge(ev)
		if p.in+p.out > 0 {
			last.in, last.out = p.in, p.out
		}
		if p.hasFinish {
			last.finish, last.hasFinish = p.finish, true
		}
		if p.text != "" {
			if err := emit(chunk(id, model, map[string]any{"content": p.text}, nil)); err != nil {
				return err
			}
		}
		for _, fc := range p.calls {
			tc := openAIToolCall(calls, newCallID(), fc)
			calls++
			if err := emit(chunk(id, model, map[string]any{"tool_calls": []any{tc}}, nil)); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil || !started {
		return err
	}
	c := chunk(id, model, map[string]any{}, last.finishReason(calls > 0))
	c["usage"] = map[string]any{"prompt_tokens": last.in, "completion_tokens": last.out, "total_tokens": last.in + last.out}
	return emit(c)
}
