package server

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"gorouter/internal/config"
	"gorouter/internal/oauth"
	"gorouter/internal/store"
	"gorouter/internal/usage"
)

func do(t *testing.T, method, url, body, key string) (int, string) {
	req, _ := http.NewRequest(method, url, strings.NewReader(body))
	if key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

// Full flow: SQLite-backed server, OAuth login against a fake IdP, token refresh,
// bearer token reaching the upstream, and a hot config reload.
func TestOAuthLoginRefreshAndHotReload(t *testing.T) {
	var tokenCalls, refreshCalls atomic.Int32
	idp := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.ParseForm()
		tokenCalls.Add(1)
		switch r.Form.Get("grant_type") {
		case "authorization_code":
			if r.Form.Get("code") != "the-code" || r.Form.Get("code_verifier") == "" {
				w.WriteHeader(400)
				return
			}
			// expires_in=1 -> inside the refresh skew, so the next use must refresh.
			io.WriteString(w, `{"access_token":"at-1","refresh_token":"rt-1","expires_in":1}`)
		case "refresh_token":
			refreshCalls.Add(1)
			if r.Form.Get("refresh_token") != "rt-1" {
				w.WriteHeader(400)
				return
			}
			io.WriteString(w, `{"access_token":"at-2","expires_in":3600}`)
		}
	}))
	defer idp.Close()

	var gotAuth atomic.Value
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth.Store(r.Header.Get("Authorization"))
		io.WriteString(w, `{"id":"1","model":"m","choices":[{"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`)
	}))
	defer up.Close()

	st, err := store.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	doc := `{"api_keys":["k"],"providers":[{"id":"sub","type":"openai","base_url":"` + up.URL + `","models":["m"],
	  "oauth":{"auth_url":"` + idp.URL + `/authorize","token_url":"` + idp.URL + `/token","client_id":"cid","scopes":["a","b"]}}]}`
	cfg, err := config.Parse([]byte(doc))
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(New(Options{Config: cfg, Store: st, OAuth: oauth.NewManager(st), Usage: usage.New(st)}).Handler())
	defer srv.Close()

	// Not connected yet -> routing fails with a clear message.
	chat := `{"model":"sub/m","messages":[{"role":"user","content":"hi"}]}`
	if code, body := do(t, "POST", srv.URL+"/v1/chat/completions", chat, "k"); code == 200 || !strings.Contains(body, "not connected") {
		t.Fatalf("want not-connected failure, got %d %s", code, body)
	}

	// Start: authorization URL carries PKCE + state.
	code, body := do(t, "POST", srv.URL+"/admin/oauth/sub/start", "", "k")
	if code != 200 {
		t.Fatalf("start: %d %s", code, body)
	}
	var start struct{ URL string }
	json.Unmarshal([]byte(body), &start)
	u, _ := url.Parse(start.URL)
	q := u.Query()
	if q.Get("code_challenge_method") != "S256" || q.Get("code_challenge") == "" || q.Get("client_id") != "cid" || q.Get("scope") != "a b" {
		t.Fatalf("bad auth url: %s", start.URL)
	}

	// Bad state is rejected; good state completes the login.
	if code, _ := do(t, "GET", srv.URL+"/oauth/callback?state=nope&code=the-code", "", ""); code != 400 {
		t.Fatalf("bad state accepted: %d", code)
	}
	if code, body := do(t, "GET", srv.URL+"/oauth/callback?state="+q.Get("state")+"&code=the-code", "", ""); code != 200 {
		t.Fatalf("callback: %d %s", code, body)
	}

	// First use: token already inside the refresh window -> refreshed to at-2.
	if code, body := do(t, "POST", srv.URL+"/v1/chat/completions", chat, "k"); code != 200 {
		t.Fatalf("chat: %d %s", code, body)
	}
	if gotAuth.Load() != "Bearer at-2" || refreshCalls.Load() != 1 {
		t.Fatalf("upstream auth=%v refreshes=%d", gotAuth.Load(), refreshCalls.Load())
	}
	// Second use reuses the fresh token.
	do(t, "POST", srv.URL+"/v1/chat/completions", chat, "k")
	if refreshCalls.Load() != 1 {
		t.Fatalf("refreshed again: %d", refreshCalls.Load())
	}
	tok, ok, _ := st.GetToken("sub")
	if !ok || tok.AccessToken != "at-2" || tok.RefreshToken != "rt-1" {
		t.Fatalf("stored token %+v (refresh token must be preserved when not rotated)", tok)
	}

	// Admin endpoints require the key.
	if code, _ := do(t, "GET", srv.URL+"/admin/status", "", ""); code != 401 {
		t.Fatalf("admin open: %d", code)
	}

	// Hot reload: invalid config rejected, valid one applied and persisted.
	if code, _ := do(t, "PUT", srv.URL+"/admin/config", `{"providers":[{"id":"x","type":"nope"}]}`, "k"); code != 400 {
		t.Fatalf("invalid config accepted: %d", code)
	}
	newDoc := `{"api_keys":["k"],"providers":[{"id":"sub","type":"openai","base_url":"` + up.URL + `","models":["m"],
	  "oauth":{"auth_url":"a","token_url":"b","client_id":"c"}}],"aliases":{"fast":"sub/m"}}`
	if code, body := do(t, "PUT", srv.URL+"/admin/config", newDoc, "k"); code != 200 {
		t.Fatalf("put: %d %s", code, body)
	}
	if code, body := do(t, "POST", srv.URL+"/v1/chat/completions", strings.Replace(chat, "sub/m", "fast", 1), "k"); code != 200 {
		t.Fatalf("alias after reload: %d %s", code, body)
	}
	raw, _ := st.RawConfig()
	if !strings.Contains(string(raw), `"fast"`) {
		t.Fatalf("config not persisted: %s", raw)
	}

	// Disconnect removes credentials.
	do(t, "DELETE", srv.URL+"/admin/oauth/sub", "", "k")
	if _, ok, _ := st.GetToken("sub"); ok {
		t.Fatal("token not deleted")
	}
}

func TestUsagePersistsInSQLite(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "u.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	tr := usage.New(st)
	tr.Add(usage.Record{Key: "a/b", In: 10, Out: 5, PriceIn: 1, PriceOut: 1})
	if err := tr.Flush(); err != nil {
		t.Fatal(err)
	}
	got := usage.New(st).Snapshot()["a/b"]
	if got.Requests != 1 || got.InTokens != 10 || got.OutTokens != 5 {
		t.Fatalf("reloaded %+v", got)
	}
}
