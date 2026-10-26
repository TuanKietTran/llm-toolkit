package server

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"gorouter/internal/config"
	"gorouter/internal/usage"
)

func TestSaverShortensToolOutputBeforeUpstream(t *testing.T) {
	var upstreamLen atomic.Int64
	var sawTerse atomic.Bool
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		upstreamLen.Store(int64(len(b)))
		sawTerse.Store(strings.Contains(string(b), "Be terse."))
		io.WriteString(w, `{"id":"1","model":"m","choices":[{"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`)
	}))
	defer up.Close()

	cfg := &config.Config{
		APIKeys:   []string{"k"},
		Providers: []config.Provider{{ID: "o", Type: "openai", BaseURL: up.URL}},
		Saver:     config.TokenSaver{Enabled: true, MaxToolChars: 500, TersePrompt: "Be terse."},
		Cooldown:  config.Cooldown{BaseSec: 60, MaxSec: 60},
	}
	cfg.ApplyDefaults()
	cfg.SetRaw([]byte(`{}`))
	srv := httptest.NewServer(New(Options{Config: cfg, Usage: usage.New(nil)}).Handler())
	defer srv.Close()

	var rows strings.Builder
	for i := 0; i < 3000; i++ {
		rows.WriteString("output row " + strings.Repeat("z", i%11) + "\n")
	}
	payload, _ := json.Marshal(map[string]any{"model": "o/m", "messages": []any{
		map[string]any{"role": "user", "content": "run it"},
		map[string]any{"role": "tool", "tool_call_id": "t", "content": rows.String()},
	}})
	if code, body := post(t, srv.URL+"/v1/chat/completions", string(payload), "Authorization", "Bearer k"); code != 200 {
		t.Fatalf("got %d %s", code, body)
	}
	if got := upstreamLen.Load(); got == 0 || got > 2000 {
		t.Fatalf("upstream body should be small after saving, got %d bytes", got)
	}
	if !sawTerse.Load() {
		t.Fatal("terse prompt not injected")
	}

	code, body := do(t, "GET", srv.URL+"/admin/status", "", "k")
	if code != 200 {
		t.Fatalf("status %d", code)
	}
	var st struct {
		Saver struct {
			Enabled bool  `json:"enabled"`
			Saved   int64 `json:"saved_chars"`
		} `json:"token_saver"`
	}
	if err := json.Unmarshal([]byte(body), &st); err != nil {
		t.Fatal(err)
	}
	if !st.Saver.Enabled || st.Saver.Saved < 10000 {
		t.Fatalf("token_saver status = %+v", st.Saver)
	}
}
