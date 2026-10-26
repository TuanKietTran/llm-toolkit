package translate

import (
	"strings"
)

const defaultMaxTokens = 4096

// OpenAIToAnthropic converts an OpenAI chat request into an Anthropic messages request.
func OpenAIToAnthropic(req map[string]any, model string) map[string]any {
	out := map[string]any{"model": model}
	var system []string
	var systemCache []any // cache_control per system entry; nil when none
	var msgs []any

	appendMsg := func(role string, blocks []any) {
		if len(blocks) == 0 {
			return
		}
		if n := len(msgs); n > 0 {
			last := msgs[n-1].(map[string]any)
			if last["role"] == role {
				last["content"] = append(last["content"].([]any), blocks...)
				return
			}
		}
		msgs = append(msgs, map[string]any{"role": role, "content": blocks})
	}

	for _, m := range asSlice(req["messages"]) {
		mm := asMap(m)
		switch str(mm["role"]) {
		case "system", "developer":
			if t := contentText(mm["content"]); t != "" {
				system = append(system, t)
				systemCache = append(systemCache, systemCacheControl(mm))
			}
		case "user":
			appendMsg("user", userBlocks(mm["content"]))
		case "assistant":
			var blocks []any
			if t := contentText(mm["content"]); t != "" {
				blocks = append(blocks, map[string]any{"type": "text", "text": t})
			}
			for _, tc := range asSlice(mm["tool_calls"]) {
				tcm := asMap(tc)
				fn := asMap(tcm["function"])
				blocks = append(blocks, map[string]any{
					"type":  "tool_use",
					"id":    str(tcm["id"]),
					"name":  str(fn["name"]),
					"input": parseJSONObject(str(fn["arguments"])),
				})
			}
			appendMsg("assistant", blocks)
		case "tool":
			appendMsg("user", []any{map[string]any{
				"type":        "tool_result",
				"tool_use_id": str(mm["tool_call_id"]),
				"content":     contentText(mm["content"]),
			}})
		}
	}
	out["messages"] = msgs
	if len(system) > 0 {
		out["system"] = anthropicSystem(system, systemCache)
	}

	maxTokens := num(req["max_tokens"])
	if maxTokens == 0 {
		maxTokens = num(req["max_completion_tokens"])
	}
	if maxTokens == 0 {
		maxTokens = defaultMaxTokens
	}
	out["max_tokens"] = maxTokens
	for _, k := range []string{"temperature", "top_p", "stream"} {
		if v, ok := req[k]; ok {
			out[k] = v
		}
	}
	switch stop := req["stop"].(type) {
	case string:
		out["stop_sequences"] = []any{stop}
	case []any:
		out["stop_sequences"] = stop
	}

	toolsOff := false
	switch tc := req["tool_choice"].(type) {
	case string:
		switch tc {
		case "auto":
			out["tool_choice"] = map[string]any{"type": "auto"}
		case "required":
			out["tool_choice"] = map[string]any{"type": "any"}
		case "none":
			toolsOff = true
		}
	case map[string]any:
		if name := str(asMap(tc["function"])["name"]); name != "" {
			out["tool_choice"] = map[string]any{"type": "tool", "name": name}
		}
	}
	if tools := asSlice(req["tools"]); len(tools) > 0 && !toolsOff {
		var at []any
		for _, t := range tools {
			fn := asMap(asMap(t)["function"])
			params := fn["parameters"]
			if params == nil {
				params = map[string]any{"type": "object", "properties": map[string]any{}}
			}
			tool := map[string]any{
				"name":         str(fn["name"]),
				"description":  str(fn["description"]),
				"input_schema": params,
			}
			if cc := asMap(asMap(t)["cache_control"]); cc != nil {
				tool["cache_control"] = cc
			}
			at = append(at, tool)
		}
		out["tools"] = at
	}
	return out
}

func userBlocks(content any) []any {
	if s, ok := content.(string); ok {
		if s == "" {
			return nil
		}
		return []any{map[string]any{"type": "text", "text": s}}
	}
	var blocks []any
	for _, p := range asSlice(content) {
		pm := asMap(p)
		switch str(pm["type"]) {
		case "text":
			if t := str(pm["text"]); t != "" {
				block := map[string]any{"type": "text", "text": t}
				if cc := asMap(pm["cache_control"]); cc != nil {
					block["cache_control"] = cc
				}
				blocks = append(blocks, block)
			}
		case "image_url":
			url := str(pm["image_url"])
			if url == "" {
				url = str(asMap(pm["image_url"])["url"])
			}
			blocks = append(blocks, map[string]any{"type": "image", "source": imageSource(url)})
		}
	}
	return blocks
}

func imageSource(url string) map[string]any {
	if rest, ok := strings.CutPrefix(url, "data:"); ok {
		if meta, data, found := strings.Cut(rest, ","); found {
			return map[string]any{
				"type":       "base64",
				"media_type": strings.TrimSuffix(meta, ";base64"),
				"data":       data,
			}
		}
	}
	return map[string]any{"type": "url", "url": url}
}

// AnthropicToOpenAI converts an inbound Anthropic messages request into the OpenAI pivot format.
func AnthropicToOpenAI(req map[string]any) map[string]any {
	out := map[string]any{"model": req["model"]}
	var msgs []any

	switch sys := req["system"].(type) {
	case string:
		if sys != "" {
			msgs = append(msgs, map[string]any{"role": "system", "content": sys})
		}
	case []any:
		if t := contentText(sys); t != "" {
			msg := map[string]any{"role": "system", "content": t}
			for _, b := range sys {
				if bm := asMap(b); bm != nil {
					if cc := asMap(bm["cache_control"]); cc != nil {
						msg["cache_control"] = cc
					}
				}
			}
			msgs = append(msgs, msg)
		}
	}

	for _, m := range asSlice(req["messages"]) {
		mm := asMap(m)
		role := str(mm["role"])
		if s, ok := mm["content"].(string); ok {
			msgs = append(msgs, map[string]any{"role": role, "content": s})
			continue
		}
		var parts []any
		var texts []string
		var toolCalls []any
		var toolMsgs []any
		for _, b := range asSlice(mm["content"]) {
			bm := asMap(b)
			switch str(bm["type"]) {
			case "text":
				texts = append(texts, str(bm["text"]))
				part := map[string]any{"type": "text", "text": str(bm["text"])}
				if cc := asMap(bm["cache_control"]); cc != nil {
					part["cache_control"] = cc
				}
				parts = append(parts, part)
			case "image":
				src := asMap(bm["source"])
				url := str(src["url"])
				if str(src["type"]) == "base64" {
					url = "data:" + str(src["media_type"]) + ";base64," + str(src["data"])
				}
				parts = append(parts, map[string]any{"type": "image_url", "image_url": map[string]any{"url": url}})
			case "tool_use":
				toolCalls = append(toolCalls, map[string]any{
					"id": str(bm["id"]), "type": "function",
					"function": map[string]any{"name": str(bm["name"]), "arguments": toJSON(bm["input"])},
				})
			case "tool_result":
				tool := map[string]any{"role": "tool", "tool_call_id": str(bm["tool_use_id"]), "content": contentText(bm["content"])}
				if cc := asMap(bm["cache_control"]); cc != nil {
					tool["cache_control"] = cc
				}
				toolMsgs = append(toolMsgs, tool)
			}
		}
		msgs = append(msgs, toolMsgs...)
		if role == "assistant" {
			msg := map[string]any{"role": "assistant", "content": strings.Join(texts, "\n")}
			if len(toolCalls) > 0 {
				msg["tool_calls"] = toolCalls
			}
			if len(texts) > 0 || len(toolCalls) > 0 {
				msgs = append(msgs, msg)
			}
		} else if len(parts) > 0 {
			msgs = append(msgs, map[string]any{"role": role, "content": parts})
		}
	}
	out["messages"] = msgs

	for _, k := range []string{"max_tokens", "temperature", "top_p", "stream", "cache_control"} {
		if v, ok := req[k]; ok {
			out[k] = v
		}
	}
	if s, ok := req["stop_sequences"]; ok {
		out["stop"] = s
	}
	if tools := asSlice(req["tools"]); len(tools) > 0 {
		var ot []any
		for _, t := range tools {
			tm := asMap(t)
			ot = append(ot, map[string]any{"type": "function", "function": map[string]any{
				"name": str(tm["name"]), "description": str(tm["description"]), "parameters": tm["input_schema"],
			}})
		}
		out["tools"] = ot
	}
	if tc := asMap(req["tool_choice"]); tc != nil {
		switch str(tc["type"]) {
		case "auto":
			out["tool_choice"] = "auto"
		case "any":
			out["tool_choice"] = "required"
		case "none":
			out["tool_choice"] = "none"
		case "tool":
			out["tool_choice"] = map[string]any{"type": "function", "function": map[string]any{"name": str(tc["name"])}}
		}
	}
	return out
}

// systemCacheControl returns the cache_control set on a system message or on its last content
// part, or nil.
func systemCacheControl(mm map[string]any) any {
	if cc := asMap(mm["cache_control"]); cc != nil {
		return cc
	}
	if parts := asSlice(mm["content"]); len(parts) > 0 {
		if cc := asMap(asMap(parts[len(parts)-1])["cache_control"]); cc != nil {
			return cc
		}
	}
	return nil
}

// anthropicSystem keeps the system prompt a plain string unless a cache_control marker has to be
// carried, in which case it becomes text blocks so Anthropic can cache the prefix up to the marker.
func anthropicSystem(system []string, cache []any) any {
	marked := false
	for _, cc := range cache {
		if cc != nil {
			marked = true
		}
	}
	if !marked {
		return strings.Join(system, "\n\n")
	}
	blocks := make([]any, 0, len(system))
	for i, t := range system {
		block := map[string]any{"type": "text", "text": t}
		if cache[i] != nil {
			block["cache_control"] = cache[i]
		}
		blocks = append(blocks, block)
	}
	return blocks
}
