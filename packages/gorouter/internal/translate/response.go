package translate

import "time"

var stopToFinish = map[string]string{
	"end_turn":      "stop",
	"stop_sequence": "stop",
	"max_tokens":    "length",
	"tool_use":      "tool_calls",
}

var finishToStop = map[string]string{
	"stop":       "end_turn",
	"length":     "max_tokens",
	"tool_calls": "tool_use",
}

// AnthropicRespToOpenAI converts a non-streaming Anthropic message into a chat.completion.
func AnthropicRespToOpenAI(resp map[string]any) map[string]any {
	var text string
	var toolCalls []any
	for _, b := range asSlice(resp["content"]) {
		bm := asMap(b)
		switch str(bm["type"]) {
		case "text":
			text += str(bm["text"])
		case "tool_use":
			toolCalls = append(toolCalls, map[string]any{
				"id": str(bm["id"]), "type": "function",
				"function": map[string]any{"name": str(bm["name"]), "arguments": toJSON(bm["input"])},
			})
		}
	}
	msg := map[string]any{"role": "assistant", "content": text}
	if len(toolCalls) > 0 {
		msg["tool_calls"] = toolCalls
		if text == "" {
			msg["content"] = nil
		}
	}
	finish := stopToFinish[str(resp["stop_reason"])]
	if finish == "" {
		finish = "stop"
	}
	u := asMap(resp["usage"])
	in, out := num(u["input_tokens"]), num(u["output_tokens"])
	return map[string]any{
		"id":      str(resp["id"]),
		"object":  "chat.completion",
		"created": time.Now().Unix(),
		"model":   str(resp["model"]),
		"choices": []any{map[string]any{"index": 0, "message": msg, "finish_reason": finish}},
		"usage":   map[string]any{"prompt_tokens": in, "completion_tokens": out, "total_tokens": in + out},
	}
}

// OpenAIRespToAnthropic converts a chat.completion into an Anthropic message.
func OpenAIRespToAnthropic(resp map[string]any) map[string]any {
	var blocks []any
	finish := "stop"
	if choices := asSlice(resp["choices"]); len(choices) > 0 {
		c := asMap(choices[0])
		msg := asMap(c["message"])
		if t := contentText(msg["content"]); t != "" {
			blocks = append(blocks, map[string]any{"type": "text", "text": t})
		}
		for _, tc := range asSlice(msg["tool_calls"]) {
			tcm := asMap(tc)
			fn := asMap(tcm["function"])
			blocks = append(blocks, map[string]any{
				"type": "tool_use", "id": str(tcm["id"]), "name": str(fn["name"]),
				"input": parseJSONObject(str(fn["arguments"])),
			})
		}
		if f := str(c["finish_reason"]); f != "" {
			finish = f
		}
	}
	if blocks == nil {
		blocks = []any{map[string]any{"type": "text", "text": ""}}
	}
	stop := finishToStop[finish]
	if stop == "" {
		stop = "end_turn"
	}
	u := asMap(resp["usage"])
	return map[string]any{
		"id":            str(resp["id"]),
		"type":          "message",
		"role":          "assistant",
		"model":         str(resp["model"]),
		"content":       blocks,
		"stop_reason":   stop,
		"stop_sequence": nil,
		"usage":         map[string]any{"input_tokens": num(u["prompt_tokens"]), "output_tokens": num(u["completion_tokens"])},
	}
}

// Usage extracts (input, output) token counts from an OpenAI-format body or chunk.
func Usage(m map[string]any) (int, int) {
	u := asMap(m["usage"])
	return num(u["prompt_tokens"]), num(u["completion_tokens"])
}
