package server

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"gorouter/internal/config"
)

type geminiSeen struct {
	path, query, apiKey, auth string
	body                      map[string]any
}

func geminiMock(seen *geminiSeen) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen.path, seen.query = r.URL.Path, r.URL.RawQuery
		seen.apiKey, seen.auth = r.Header.Get("x-goog-api-key"), r.Header.Get("Authorization")
		json.NewDecoder(r.Body).Decode(&seen.body)
		if strings.Contains(r.URL.Path, ":streamGenerateContent") {
			w.Header().Set("Content-Type", "text/event-stream")
			fmt.Fprint(w, "data: {\"candidates\":[{\"content\":{\"parts\":[{\"text\":\"he\"}]}}]}\n\n")
			fmt.Fprint(w, "data: {\"candidates\":[{\"content\":{\"parts\":[{\"text\":\"llo\"}]},\"finishReason\":\"STOP\"}],\"usageMetadata\":{\"promptTokenCount\":7,\"candidatesTokenCount\":2}}\n\n")
			return
		}
		io.WriteString(w, `{"candidates":[{"content":{"parts":[{"text":"hello"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":7,"candidatesTokenCount":2}}`)
	}))
}

func TestGeminiProviderEndToEnd(t *testing.T) {
	var seen geminiSeen
	up := geminiMock(&seen)
	defer up.Close()
	srv := newServer(t, []config.Provider{{ID: "g", Type: "gemini", BaseURL: up.URL, APIKey: "gk"}}, nil)
	defer srv.Close()

	code, body := post(t, srv.URL+"/v1/chat/completions", `{"model":"g/gemini-x","messages":[{"role":"system","content":"s"},{"role":"user","content":"hi"}]}`)
	if code != 200 || !strings.Contains(body, `"content":"hello"`) || !strings.Contains(body, `"prompt_tokens":7`) {
		t.Fatalf("unary openai: %d %s", code, body)
	}
	if seen.path != "/models/gemini-x:generateContent" || seen.apiKey != "gk" || seen.auth != "" {
		t.Fatalf("request: %+v", seen)
	}
	if seen.body["systemInstruction"] == nil || seen.body["contents"] == nil {
		t.Fatalf("body: %v", seen.body)
	}

	code, body = post(t, srv.URL+"/v1/chat/completions", `{"model":"g/gemini-x","stream":true,"messages":[{"role":"user","content":"hi"}]}`)
	if code != 200 || seen.path != "/models/gemini-x:streamGenerateContent" || seen.query != "alt=sse" ||
		!strings.Contains(body, `"content":"he"`) || !strings.Contains(body, `"content":"llo"`) || !strings.HasSuffix(strings.TrimSpace(body), "[DONE]") {
		t.Fatalf("stream openai: %d %s (%+v)", code, body, seen)
	}

	code, body = post(t, srv.URL+"/v1/messages", `{"model":"g/gemini-x","max_tokens":10,"messages":[{"role":"user","content":"hi"}]}`)
	if code != 200 || !strings.Contains(body, `"type":"message"`) || !strings.Contains(body, `"hello"`) || !strings.Contains(body, `"input_tokens":7`) {
		t.Fatalf("unary anthropic: %d %s", code, body)
	}
	code, body = post(t, srv.URL+"/v1/messages", `{"model":"g/gemini-x","max_tokens":10,"stream":true,"messages":[{"role":"user","content":"hi"}]}`)
	for _, want := range []string{"event: message_start", `"text":"he"`, `"text":"llo"`, "event: message_stop"} {
		if code != 200 || !strings.Contains(body, want) {
			t.Fatalf("stream anthropic missing %q: %d %s", want, code, body)
		}
	}
}

func TestVertexUsesBearerAuth(t *testing.T) {
	var seen geminiSeen
	up := geminiMock(&seen)
	defer up.Close()
	srv := newServer(t, []config.Provider{{ID: "v", Type: "vertex", BaseURL: up.URL + "/v1/projects/p/locations/l/publishers/google", APIKey: "tok"}}, nil)
	defer srv.Close()
	code, body := post(t, srv.URL+"/v1/chat/completions", `{"model":"v/gemini-x","messages":[{"role":"user","content":"hi"}]}`)
	if code != 200 || !strings.Contains(body, "hello") {
		t.Fatalf("%d %s", code, body)
	}
	if seen.auth != "Bearer tok" || seen.apiKey != "" || seen.path != "/v1/projects/p/locations/l/publishers/google/models/gemini-x:generateContent" {
		t.Fatalf("request: %+v", seen)
	}
}
