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
	"gorouter/internal/usage"
)

func openAIUpstream(t *testing.T, status int, hits *int) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		*hits++
		if status != 200 {
			w.WriteHeader(status)
			io.WriteString(w, `{"error":"boom"}`)
			return
		}
		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)
		if body["stream"] == true {
			w.Header().Set("Content-Type", "text/event-stream")
			io.WriteString(w, "data: {\"id\":\"1\",\"model\":\"m\",\"choices\":[{\"delta\":{\"content\":\"hi\"}}]}\n\n")
			io.WriteString(w, "data: {\"id\":\"1\",\"model\":\"m\",\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":3,\"completion_tokens\":1}}\n\n")
			io.WriteString(w, "data: [DONE]\n\n")
			return
		}
		fmt.Fprintf(w, `{"id":"1","model":%q,"choices":[{"message":{"role":"assistant","content":"hello"},"finish_reason":"stop"}],"usage":{"prompt_tokens":3,"completion_tokens":1}}`, body["model"])
	}))
}

func anthropicUpstream(hits *int) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		*hits++
		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)
		if body["stream"] == true {
			w.Header().Set("Content-Type", "text/event-stream")
			for _, e := range []string{
				`{"type":"message_start","message":{"id":"a1","model":"claude","usage":{"input_tokens":5}}}`,
				`{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
				`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"yo"}}`,
				`{"type":"content_block_stop","index":0}`,
				`{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":2}}`,
				`{"type":"message_stop"}`,
			} {
				fmt.Fprintf(w, "data: %s\n\n", e)
			}
			return
		}
		io.WriteString(w, `{"id":"a1","model":"claude","content":[{"type":"text","text":"yo"}],"stop_reason":"end_turn","usage":{"input_tokens":5,"output_tokens":2}}`)
	}))
}

func newServer(t *testing.T, providers []config.Provider, combos []config.Combo, keys ...string) *httptest.Server {
	cfg := &config.Config{APIKeys: keys, Providers: providers, Combos: combos, Cooldown: config.Cooldown{BaseSec: 60, MaxSec: 60}}
	cfg.ApplyDefaults()
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	cfg.SetRaw([]byte(`{}`))
	s := New(Options{Config: cfg, Usage: usage.New(nil)})
	return httptest.NewServer(s.Handler())
}

func post(t *testing.T, url, body string, hdr ...string) (int, string) {
	req, _ := http.NewRequest("POST", url, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	for i := 0; i+1 < len(hdr); i += 2 {
		req.Header.Set(hdr[i], hdr[i+1])
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

func TestFallbackOnRateLimit(t *testing.T) {
	var bad, good int
	u1 := openAIUpstream(t, 429, &bad)
	u2 := openAIUpstream(t, 200, &good)
	defer u1.Close()
	defer u2.Close()
	srv := newServer(t,
		[]config.Provider{
			{ID: "a", Type: "openai", BaseURL: u1.URL},
			{ID: "b", Type: "openai", BaseURL: u2.URL},
		},
		[]config.Combo{{Name: "combo", Strategy: "fallback", Models: []string{"a/x", "b/y"}}})
	defer srv.Close()

	req, _ := http.NewRequest("POST", srv.URL+"/v1/chat/completions", strings.NewReader(`{"model":"combo","messages":[{"role":"user","content":"hi"}]}`))
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	bodyBytes, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 || resp.Header.Get("X-Gorouter-Attempts") != "2" || !strings.Contains(string(bodyBytes), `"hello"`) || !strings.Contains(string(bodyBytes), `"y"`) {
		t.Fatalf("got %d attempts=%q %s", resp.StatusCode, resp.Header.Get("X-Gorouter-Attempts"), bodyBytes)
	}
	code, body := post(t, srv.URL+"/v1/chat/completions", `{"model":"combo","messages":[{"role":"user","content":"hi"}]}`)
	if code != 200 || !strings.Contains(body, `"hello"`) || !strings.Contains(body, `"y"`) {
		t.Fatalf("got %d %s", code, body)
	}
	if bad != 1 {
		t.Fatalf("provider a should be on cooldown after first failure, hits=%d", bad)
	}
}

func TestRawResponseIncludesFallbackAttemptCount(t *testing.T) {
	var bad, good int
	u1 := openAIUpstream(t, 429, &bad)
	u2 := openAIUpstream(t, 200, &good)
	defer u1.Close()
	defer u2.Close()
	srv := newServer(t,
		[]config.Provider{
			{ID: "a", Type: "openai", BaseURL: u1.URL},
			{ID: "b", Type: "openai", BaseURL: u2.URL},
		},
		[]config.Combo{{Name: "embed-combo", Strategy: "fallback", Models: []string{"a/embed", "b/embed"}}})
	defer srv.Close()

	req, err := http.NewRequest("POST", srv.URL+"/v1/embeddings", strings.NewReader(`{"model":"embed-combo","input":["hello"]}`))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 || resp.Header.Get("X-Gorouter-Attempts") != "2" || bad != 1 || good != 1 {
		t.Fatalf("status=%d attempts=%q bad=%d good=%d", resp.StatusCode, resp.Header.Get("X-Gorouter-Attempts"), bad, good)
	}
}

func TestClientErrorNotRetried(t *testing.T) {
	var bad, good int
	u1 := openAIUpstream(t, 400, &bad)
	u2 := openAIUpstream(t, 200, &good)
	defer u1.Close()
	defer u2.Close()
	srv := newServer(t,
		[]config.Provider{{ID: "a", Type: "openai", BaseURL: u1.URL}, {ID: "b", Type: "openai", BaseURL: u2.URL}},
		[]config.Combo{{Name: "combo", Models: []string{"a/x", "b/y"}}})
	defer srv.Close()
	code, _ := post(t, srv.URL+"/v1/chat/completions", `{"model":"combo","messages":[]}`)
	if code != 400 || good != 0 {
		t.Fatalf("code=%d good=%d", code, good)
	}
}

func TestAuth(t *testing.T) {
	srv := newServer(t, nil, nil, "sk-test")
	defer srv.Close()
	if code, _ := post(t, srv.URL+"/v1/chat/completions", `{}`); code != 401 {
		t.Fatalf("want 401 got %d", code)
	}
	if code, _ := post(t, srv.URL+"/v1/chat/completions", `{}`, "Authorization", "Bearer sk-test"); code != 400 {
		t.Fatalf("want 400 (past auth) got %d", code)
	}
}

func TestAnthropicClientToOpenAIUpstream(t *testing.T) {
	var hits int
	u := openAIUpstream(t, 200, &hits)
	defer u.Close()
	srv := newServer(t, []config.Provider{{ID: "o", Type: "openai", BaseURL: u.URL}}, nil)
	defer srv.Close()

	code, body := post(t, srv.URL+"/v1/messages", `{"model":"o/m","max_tokens":10,"system":"be nice","messages":[{"role":"user","content":"hi"}]}`)
	if code != 200 || !strings.Contains(body, `"type":"message"`) || !strings.Contains(body, `"hello"`) {
		t.Fatalf("got %d %s", code, body)
	}
	code, body = post(t, srv.URL+"/v1/messages", `{"model":"o/m","max_tokens":10,"stream":true,"messages":[{"role":"user","content":"hi"}]}`)
	for _, want := range []string{"event: message_start", "text_delta", "event: message_stop"} {
		if code != 200 || !strings.Contains(body, want) {
			t.Fatalf("missing %q in %d %s", want, code, body)
		}
	}
}

func TestOpenAIClientToAnthropicUpstream(t *testing.T) {
	var hits int
	u := anthropicUpstream(&hits)
	defer u.Close()
	srv := newServer(t, []config.Provider{{ID: "c", Type: "anthropic", BaseURL: u.URL}}, nil)
	defer srv.Close()

	code, body := post(t, srv.URL+"/v1/chat/completions", `{"model":"c/claude","messages":[{"role":"user","content":"hi"}]}`)
	if code != 200 || !strings.Contains(body, `"content":"yo"`) || !strings.Contains(body, `"prompt_tokens":5`) {
		t.Fatalf("got %d %s", code, body)
	}
	code, body = post(t, srv.URL+"/v1/chat/completions", `{"model":"c/claude","stream":true,"messages":[{"role":"user","content":"hi"}]}`)
	if code != 200 || !strings.Contains(body, `"content":"yo"`) || !strings.HasSuffix(strings.TrimSpace(body), "[DONE]") {
		t.Fatalf("got %d %s", code, body)
	}
}

func TestStreamOpenAIPassthrough(t *testing.T) {
	var hits int
	u := openAIUpstream(t, 200, &hits)
	defer u.Close()
	srv := newServer(t, []config.Provider{{ID: "o", Type: "openai", BaseURL: u.URL}}, nil)
	defer srv.Close()
	code, body := post(t, srv.URL+"/v1/chat/completions", `{"model":"o/m","stream":true,"messages":[]}`)
	if code != 200 || strings.Count(body, "data: ") != 3 {
		t.Fatalf("got %d %s", code, body)
	}
}
