package server

import (
	"encoding/json"
	"gorouter/internal/store"
	"gorouter/internal/usage"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"gorouter/internal/config"
)

// keyedUpstream rejects (status) requests carrying badKey and records which keys it saw.
func keyedUpstream(badKey string, badStatus int) (*httptest.Server, *[]string) {
	var mu sync.Mutex
	var seen []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		k := r.Header.Get("Authorization")
		mu.Lock()
		seen = append(seen, k)
		mu.Unlock()
		if k == "Bearer "+badKey {
			w.WriteHeader(badStatus)
			return
		}
		io.WriteString(w, `{"id":"1","model":"m","choices":[{"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":7,"completion_tokens":3}}`)
	}))
	return srv, &seen
}

const chatBody = `{"model":"p/m","messages":[{"role":"user","content":"hi"}]}`

func TestAccountFailoverAndCooldown(t *testing.T) {
	up, seen := keyedUpstream("k1", 429)
	defer up.Close()
	srv := newServer(t, []config.Provider{{ID: "p", Type: "openai", BaseURL: up.URL, Accounts: []config.Account{{ID: "a", APIKey: "k1"}, {ID: "b", APIKey: "k2"}}, AccountStrategy: "fallback"}}, nil)
	defer srv.Close()
	for i := 0; i < 3; i++ {
		if code, body := post(t, srv.URL+"/v1/chat/completions", chatBody); code != 200 {
			t.Fatalf("req %d: %d %s", i, code, body)
		}
	}
	// Account a is tried once (429 -> cooldown), then skipped; b serves all three.
	got := map[string]int{}
	for _, k := range *seen {
		got[k]++
	}
	if got["Bearer k1"] != 1 || got["Bearer k2"] != 3 {
		t.Fatalf("calls per key: %v", got)
	}
}

func TestAccountRoundRobin(t *testing.T) {
	up, seen := keyedUpstream("none", 500)
	defer up.Close()
	srv := newServer(t, []config.Provider{{ID: "p", Type: "openai", BaseURL: up.URL, Accounts: []config.Account{{ID: "a", APIKey: "k1"}, {ID: "b", APIKey: "k2"}}, AccountStrategy: "round_robin"}}, nil)
	defer srv.Close()
	for i := 0; i < 4; i++ {
		post(t, srv.URL+"/v1/chat/completions", chatBody)
	}
	got := map[string]int{}
	for _, k := range *seen {
		got[k]++
	}
	if got["Bearer k1"] != 2 || got["Bearer k2"] != 2 {
		t.Fatalf("round robin uneven: %v", got)
	}
}

func TestQuotaMovesToNextAccount(t *testing.T) {
	up, seen := keyedUpstream("none", 500)
	defer up.Close()
	q, _ := time_parse("1h")
	srv := newServer(t, []config.Provider{{ID: "p", Type: "openai", BaseURL: up.URL,
		Accounts: []config.Account{{ID: "a", APIKey: "k1"}, {ID: "b", APIKey: "k2"}}, AccountStrategy: "fallback",
		Quotas: []config.Quota{{Window: "1h", Duration: q, MaxRequests: 2}}}}, nil)
	defer srv.Close()
	for i := 0; i < 3; i++ {
		post(t, srv.URL+"/v1/chat/completions", chatBody)
	}
	got := map[string]int{}
	for _, k := range *seen {
		got[k]++
	}
	// a serves 2 then is out of quota; third request goes to b.
	if got["Bearer k1"] != 2 || got["Bearer k2"] != 1 {
		t.Fatalf("quota routing: %v", got)
	}
	resp, err := http.Get(srv.URL + "/admin/status")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var st struct {
		Quotas []struct {
			Account  string `json:"account"`
			Requests int    `json:"requests"`
			Exhaust  bool   `json:"exhausted"`
		} `json:"quotas"`
	}
	json.NewDecoder(resp.Body).Decode(&st)
	if len(st.Quotas) != 2 || st.Quotas[0].Account != "p#a" || st.Quotas[0].Requests != 2 || !st.Quotas[0].Exhaust {
		t.Fatalf("quota status: %+v", st.Quotas)
	}
}

func time_parse(s string) (d time.Duration, err error) { return time.ParseDuration(s) }

func TestQuotaSurvivesServerRestartViaSQLite(t *testing.T) {
	up, _ := keyedUpstream("none", 500)
	defer up.Close()
	st, err := store.Open(filepath.Join(t.TempDir(), "q.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	d, _ := time.ParseDuration("1h")
	mk := func() (*httptest.Server, *Server) {
		cfg := &config.Config{Providers: []config.Provider{{ID: "p", Type: "openai", BaseURL: up.URL,
			Accounts: []config.Account{{ID: "a", APIKey: "k1"}, {ID: "b", APIKey: "k2"}}, AccountStrategy: "fallback",
			Quotas: []config.Quota{{Window: "1h", Duration: d, MaxRequests: 1}}}}, Cooldown: config.Cooldown{BaseSec: 60, MaxSec: 60}}
		cfg.ApplyDefaults()
		cfg.SetRaw([]byte(`{}`))
		s := New(Options{Config: cfg, Usage: usage.New(nil), Quotas: st})
		return httptest.NewServer(s.Handler()), s
	}

	srv1, s1 := mk()
	post(t, srv1.URL+"/v1/chat/completions", chatBody) // consumes account a's only request
	if err := s1.rt().Quota.Flush(); err != nil {
		t.Fatal(err)
	}
	srv1.Close()

	srv2, s2 := mk() // new process, same database
	defer srv2.Close()
	_ = s2
	resp, _ := http.Post(srv2.URL+"/v1/chat/completions", "application/json", strings.NewReader(chatBody))
	if resp.Header.Get("X-Gorouter-Target") != "p/m@b" {
		t.Fatalf("after restart expected account b, got %q", resp.Header.Get("X-Gorouter-Target"))
	}
}

func TestFailedAttemptsAreCountedAsErrors(t *testing.T) {
	up, _ := keyedUpstream("k1", 500)
	defer up.Close()
	srv := newServer(t, []config.Provider{{ID: "p", Type: "openai", BaseURL: up.URL, APIKey: "k1"}}, nil)
	defer srv.Close()
	if code, _ := post(t, srv.URL+"/v1/chat/completions", chatBody); code < 500 {
		t.Fatalf("want upstream failure, got %d", code)
	}
	resp, err := http.Get(srv.URL + "/admin/usage")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var u map[string]struct{ Requests, Errors int }
	json.NewDecoder(resp.Body).Decode(&u)
	if u["p/m"].Errors != 1 || u["p/m"].Requests != 1 {
		t.Fatalf("usage: %+v", u)
	}
}
