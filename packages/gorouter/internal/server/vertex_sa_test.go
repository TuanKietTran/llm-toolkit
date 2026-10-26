package server

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"gorouter/internal/config"
)

func TestVertexServiceAccountEndToEnd(t *testing.T) {
	var gotAuth, gotPath atomic.Value
	var mints atomic.Int32
	idp := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mints.Add(1)
		io.WriteString(w, `{"access_token":"ya29.sa","expires_in":3600}`)
	}))
	defer idp.Close()
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth.Store(r.Header.Get("Authorization"))
		gotPath.Store(r.URL.Path)
		io.WriteString(w, `{"candidates":[{"content":{"parts":[{"text":"hi"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":1,"candidatesTokenCount":1}}`)
	}))
	defer up.Close()

	pk, _ := rsa.GenerateKey(rand.Reader, 2048)
	der, _ := x509.MarshalPKCS8PrivateKey(pk)
	sa, _ := json.Marshal(map[string]string{
		"client_email": "sa@p.iam.gserviceaccount.com", "token_uri": idp.URL,
		"private_key": string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})),
	})

	srv := newServer(t, []config.Provider{{ID: "v", Type: "vertex", BaseURL: up.URL, ServiceAccount: string(sa)}}, nil)
	defer srv.Close()
	for i := 0; i < 2; i++ {
		code, body := post(t, srv.URL+"/v1/chat/completions", `{"model":"v/gemini-2.5-flash","messages":[{"role":"user","content":"x"}]}`)
		if code != 200 || !strings.Contains(body, `"hi"`) {
			t.Fatalf("got %d %s", code, body)
		}
	}
	if gotAuth.Load() != "Bearer ya29.sa" || !strings.HasSuffix(gotPath.Load().(string), "gemini-2.5-flash:generateContent") {
		t.Fatalf("auth=%v path=%v", gotAuth.Load(), gotPath.Load())
	}
	if mints.Load() != 1 {
		t.Fatalf("minted %d tokens, want 1 (cached)", mints.Load())
	}
}

func TestVertexProjectShorthandAndSAValidation(t *testing.T) {
	cfg, err := config.Parse([]byte(`{"providers":[{"id":"v","type":"vertex","project":"my-proj","location":"europe-west4","api_key":"tok"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	want := "https://europe-west4-aiplatform.googleapis.com/v1/projects/my-proj/locations/europe-west4/publishers/google"
	if cfg.Providers[0].BaseURL != want {
		t.Fatalf("base_url=%q", cfg.Providers[0].BaseURL)
	}
	if _, err := config.Parse([]byte(`{"providers":[{"id":"v","type":"vertex","project":"p","service_account":"/no/such/file.json"}]}`)); err == nil {
		t.Fatal("bad service account path must fail validation")
	}
	if _, err := config.Parse([]byte(`{"providers":[{"id":"o","type":"openai","base_url":"http://x","service_account":"{}"}]}`)); err == nil {
		t.Fatal("service_account on non-vertex must fail validation")
	}
}
