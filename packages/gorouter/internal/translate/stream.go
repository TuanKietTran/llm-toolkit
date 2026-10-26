package translate

import (
	"encoding/json"
	"fmt"
	"io"
	"time"
)

func chunk(id, model string, delta map[string]any, finish any) map[string]any {
	return map[string]any{
		"id":      id,
		"object":  "chat.completion.chunk",
		"created": time.Now().Unix(),
		"model":   model,
		"choices": []any{map[string]any{"index": 0, "delta": delta, "finish_reason": finish}},
	}
}

// AnthropicStreamToOpenAI reads an Anthropic SSE stream and emits OpenAI chunks.
func AnthropicStreamToOpenAI(r io.Reader, emit func(map[string]any) error) error {
	var id, model string
	var inTokens int
	toolIdx := map[int]int{}
	next := 0
	return ReadSSE(r, func(event, data string) error {
		var ev map[string]any
		if err := json.Unmarshal([]byte(data), &ev); err != nil {
			return nil
		}
		typ := str(ev["type"])
		if typ == "" {
			typ = event
		}
		switch typ {
		case "message_start":
			m := asMap(ev["message"])
			id, model = str(m["id"]), str(m["model"])
			inTokens = num(asMap(m["usage"])["input_tokens"])
			return emit(chunk(id, model, map[string]any{"role": "assistant", "content": ""}, nil))
		case "content_block_start":
			cb := asMap(ev["content_block"])
			if str(cb["type"]) == "tool_use" {
				i := next
				next++
				toolIdx[num(ev["index"])] = i
				return emit(chunk(id, model, map[string]any{"tool_calls": []any{map[string]any{
					"index": i, "id": str(cb["id"]), "type": "function",
					"function": map[string]any{"name": str(cb["name"]), "arguments": ""},
				}}}, nil))
			}
		case "content_block_delta":
			d := asMap(ev["delta"])
			switch str(d["type"]) {
			case "text_delta":
				return emit(chunk(id, model, map[string]any{"content": str(d["text"])}, nil))
			case "input_json_delta":
				return emit(chunk(id, model, map[string]any{"tool_calls": []any{map[string]any{
					"index":    toolIdx[num(ev["index"])],
					"function": map[string]any{"arguments": str(d["partial_json"])},
				}}}, nil))
			}
		case "message_delta":
			finish := stopToFinish[str(asMap(ev["delta"])["stop_reason"])]
			if finish == "" {
				finish = "stop"
			}
			out := num(asMap(ev["usage"])["output_tokens"])
			c := chunk(id, model, map[string]any{}, finish)
			c["usage"] = map[string]any{"prompt_tokens": inTokens, "completion_tokens": out, "total_tokens": inTokens + out}
			return emit(c)
		case "error":
			return fmt.Errorf("upstream stream error: %s", str(asMap(ev["error"])["message"]))
		}
		return nil
	})
}

// OpenAIStreamToChunks reads an OpenAI SSE stream and emits parsed chunks.
func OpenAIStreamToChunks(r io.Reader, emit func(map[string]any) error) error {
	return ReadSSE(r, func(_, data string) error {
		if data == "[DONE]" {
			return nil
		}
		var c map[string]any
		if err := json.Unmarshal([]byte(data), &c); err != nil {
			return nil
		}
		return emit(c)
	})
}

// AnthropicWriter renders OpenAI chunks as an Anthropic SSE stream.
type AnthropicWriter struct {
	w          io.Writer
	flush      func()
	started    bool
	open       string // "", "text" or "tool"
	block      int
	toolBlocks map[int]int
	finish     string
	in, out    int
	id, model  string
}

func NewAnthropicWriter(w io.Writer, flush func()) *AnthropicWriter {
	return &AnthropicWriter{w: w, flush: flush, toolBlocks: map[int]int{}}
}

func (a *AnthropicWriter) send(event string, v map[string]any) error {
	v["type"] = event
	_, err := fmt.Fprintf(a.w, "event: %s\ndata: %s\n\n", event, toJSON(v))
	a.flush()
	return err
}

func (a *AnthropicWriter) closeBlock() error {
	if a.open == "" {
		return nil
	}
	a.open = ""
	err := a.send("content_block_stop", map[string]any{"index": a.block})
	a.block++
	return err
}

func (a *AnthropicWriter) start() error {
	if a.started {
		return nil
	}
	a.started = true
	return a.send("message_start", map[string]any{"message": map[string]any{
		"id": a.id, "type": "message", "role": "assistant", "model": a.model, "content": []any{},
		"stop_reason": nil, "usage": map[string]any{"input_tokens": a.in, "output_tokens": 0},
	}})
}

func (a *AnthropicWriter) Write(c map[string]any) error {
	if a.id == "" {
		a.id, a.model = str(c["id"]), str(c["model"])
	}
	if in, out := Usage(c); in+out > 0 {
		a.in, a.out = in, out
	}
	if err := a.start(); err != nil {
		return err
	}
	choices := asSlice(c["choices"])
	if len(choices) == 0 {
		return nil
	}
	ch := asMap(choices[0])
	d := asMap(ch["delta"])
	if t := str(d["content"]); t != "" {
		if a.open != "text" {
			if err := a.closeBlock(); err != nil {
				return err
			}
			a.open = "text"
			if err := a.send("content_block_start", map[string]any{"index": a.block, "content_block": map[string]any{"type": "text", "text": ""}}); err != nil {
				return err
			}
		}
		if err := a.send("content_block_delta", map[string]any{"index": a.block, "delta": map[string]any{"type": "text_delta", "text": t}}); err != nil {
			return err
		}
	}
	for _, tc := range asSlice(d["tool_calls"]) {
		tcm := asMap(tc)
		fn := asMap(tcm["function"])
		i := num(tcm["index"])
		if name := str(fn["name"]); name != "" {
			if err := a.closeBlock(); err != nil {
				return err
			}
			a.open = "tool"
			a.toolBlocks[i] = a.block
			if err := a.send("content_block_start", map[string]any{"index": a.block, "content_block": map[string]any{
				"type": "tool_use", "id": str(tcm["id"]), "name": name, "input": map[string]any{},
			}}); err != nil {
				return err
			}
		}
		if args := str(fn["arguments"]); args != "" && a.open == "tool" {
			if err := a.send("content_block_delta", map[string]any{"index": a.toolBlocks[i], "delta": map[string]any{"type": "input_json_delta", "partial_json": args}}); err != nil {
				return err
			}
		}
	}
	if f := str(ch["finish_reason"]); f != "" {
		a.finish = f
	}
	return nil
}

// Close finishes the Anthropic stream. Safe to call when nothing was written.
func (a *AnthropicWriter) Close() error {
	if err := a.start(); err != nil {
		return err
	}
	if err := a.closeBlock(); err != nil {
		return err
	}
	stop := finishToStop[a.finish]
	if stop == "" {
		stop = "end_turn"
	}
	if err := a.send("message_delta", map[string]any{
		"delta": map[string]any{"stop_reason": stop, "stop_sequence": nil},
		"usage": map[string]any{"output_tokens": a.out},
	}); err != nil {
		return err
	}
	return a.send("message_stop", map[string]any{})
}
