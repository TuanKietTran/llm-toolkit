// Command mockupstream is a tiny OpenAI-compatible server for trying gorouter without real
// providers. /bad/* always answers 429 so fallback and cooldown can be seen live.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"
)

func main() {
	addr := flag.String("addr", "127.0.0.1:9911", "listen address")
	flag.Parse()
	mux := http.NewServeMux()
	mux.HandleFunc("POST /bad/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "10")
		http.Error(w, `{"error":"rate limited (mock)"}`, http.StatusTooManyRequests)
	})
	mux.HandleFunc("POST /chat/completions", chat)
	mux.HandleFunc("POST /embeddings", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"object":"list","data":[{"embedding":[0.1,0.2,0.3],"index":0}],"usage":{"prompt_tokens":3}}`)
	})
	log.Printf("mock upstream on %s", *addr)
	log.Fatal(http.ListenAndServe(*addr, mux))
}

func chat(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Model    string `json:"model"`
		Stream   bool   `json:"stream"`
		Messages []struct {
			Role    string `json:"role"`
			Content any    `json:"content"`
		} `json:"messages"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	last := ""
	for _, m := range body.Messages {
		if s, ok := m.Content.(string); ok && m.Role == "user" {
			last = s
		}
	}
	reply := fmt.Sprintf("[mock %s] you said: %q", body.Model, last)
	if !body.Stream {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id": "mock-1", "model": body.Model,
			"choices": []any{map[string]any{"message": map[string]any{"role": "assistant", "content": reply}, "finish_reason": "stop"}},
			"usage":   map[string]any{"prompt_tokens": len(last)/4 + 1, "completion_tokens": len(reply) / 4},
		})
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	fl := w.(http.Flusher)
	send := func(v any) {
		b, _ := json.Marshal(v)
		fmt.Fprintf(w, "data: %s\n\n", b)
		fl.Flush()
	}
	chunk := func(delta map[string]any, finish any) map[string]any {
		return map[string]any{"id": "mock-1", "model": body.Model, "choices": []any{map[string]any{"delta": delta, "finish_reason": finish}}}
	}
	for _, word := range strings.Fields(reply) {
		send(chunk(map[string]any{"content": word + " "}, nil))
		time.Sleep(60 * time.Millisecond)
	}
	done := chunk(map[string]any{}, "stop")
	done["usage"] = map[string]any{"prompt_tokens": len(last)/4 + 1, "completion_tokens": len(reply) / 4}
	send(done)
	fmt.Fprint(w, "data: [DONE]\n\n")
	fl.Flush()
}
