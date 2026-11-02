package server

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"gorouter/internal/config"
	"gorouter/internal/usage"
)

func newLimitedServer(t *testing.T, maxBytes int64, keys ...string) *httptest.Server {
	cfg := &config.Config{APIKeys: keys, MaxRequestBytes: maxBytes,
		Providers: []config.Provider{{ID: "o", Type: "openai", BaseURL: "http://127.0.0.1:1", Models: []string{"m"}}},
		Cooldown:  config.Cooldown{BaseSec: 60, MaxSec: 60}}
	cfg.ApplyDefaults()
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	cfg.SetRaw([]byte(`{}`))
	return httptest.NewServer(New(Options{Config: cfg, Usage: usage.New(nil)}).Handler())
}

func TestRequestIDHeader(t *testing.T) {
	srv := newServer(t, nil, nil)
	defer srv.Close()
	resp, err := http.Get(srv.URL + "/health")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if id := resp.Header.Get("X-Request-Id"); !strings.HasPrefix(id, "req-") || len(id) != 28 {
		t.Fatalf("bad request id %q", id)
	}
}

func TestModelEndpoint(t *testing.T) {
	srv := newLimitedServer(t, 0)
	defer srv.Close()
	get := func(p string) (int, string) {
		resp, err := http.Get(srv.URL + p)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(b)
	}
	if code, body := get("/v1/models/o/m"); code != 200 || !strings.Contains(body, `"id":"o/m"`) {
		t.Fatalf("got %d %s", code, body)
	}
	if code, body := get("/v1/models/nope"); code != 404 || !strings.Contains(body, "model_not_found") {
		t.Fatalf("got %d %s", code, body)
	}
}

func TestUnauthorizedSetsChallenge(t *testing.T) {
	srv := newServer(t, nil, nil, "sk-test")
	defer srv.Close()
	req, _ := http.NewRequest("POST", srv.URL+"/v1/chat/completions", strings.NewReader(`{}`))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 401 || resp.Header.Get("WWW-Authenticate") != "Bearer" {
		t.Fatalf("status=%d challenge=%q", resp.StatusCode, resp.Header.Get("WWW-Authenticate"))
	}
}

func TestBodyTooLarge(t *testing.T) {
	srv := newLimitedServer(t, 64)
	defer srv.Close()
	big := `{"model":"o/m","messages":[{"role":"user","content":"` + strings.Repeat("x", 200) + `"}]}`
	for _, path := range []string{"/v1/chat/completions", "/v1/messages", "/v1/embeddings"} {
		if code, body := post(t, srv.URL+path, big); code != 413 || !strings.Contains(body, "too large") {
			t.Fatalf("%s: got %d %s", path, code, body)
		}
	}
}

func TestMaxRequestBytesDefault(t *testing.T) {
	s := &Server{}
	s.st.Store(&state{cfg: &config.Config{}})
	if s.maxBody() != maxBody {
		t.Fatalf("default = %d", s.maxBody())
	}
}
