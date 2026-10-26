package gcpauth

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func testKey(t *testing.T, tokenURI string) (json string, pub *rsa.PublicKey) {
	pk, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	der, _ := x509.MarshalPKCS8PrivateKey(pk)
	pemKey := string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}))
	b, _ := jsonMarshal(map[string]string{"client_email": "sa@proj.iam.gserviceaccount.com", "private_key": pemKey, "token_uri": tokenURI})
	return string(b), &pk.PublicKey
}

func jsonMarshal(v any) ([]byte, error) { return json.Marshal(v) }

func TestTokenMintVerifyAndCache(t *testing.T) {
	var calls atomic.Int32
	var pub *rsa.PublicKey
	idp := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		r.ParseForm()
		if r.Form.Get("grant_type") != "urn:ietf:params:oauth:grant-type:jwt-bearer" {
			w.WriteHeader(400)
			return
		}
		parts := strings.Split(r.Form.Get("assertion"), ".")
		if len(parts) != 3 {
			w.WriteHeader(400)
			return
		}
		sig, _ := base64.RawURLEncoding.DecodeString(parts[2])
		sum := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
		if rsa.VerifyPKCS1v15(pub, crypto.SHA256, sum[:], sig) != nil {
			w.WriteHeader(401)
			return
		}
		cb, _ := base64.RawURLEncoding.DecodeString(parts[1])
		var c map[string]any
		json.Unmarshal(cb, &c)
		if c["iss"] != "sa@proj.iam.gserviceaccount.com" || c["scope"] != Scope || c["exp"].(float64) <= c["iat"].(float64) {
			w.WriteHeader(400)
			return
		}
		w.Write([]byte(`{"access_token":"ya29.test","expires_in":3600}`))
	}))
	defer idp.Close()

	var spec string
	spec, pub = testKey(t, idp.URL)
	reg := NewRegistry()
	for i := 0; i < 3; i++ {
		tok, err := reg.Token(context.Background(), "v#default", spec)
		if err != nil || tok != "ya29.test" {
			t.Fatalf("tok=%q err=%v", tok, err)
		}
	}
	if calls.Load() != 1 {
		t.Fatalf("token endpoint called %d times, want 1 (cached)", calls.Load())
	}
}

func TestLoadErrors(t *testing.T) {
	for name, spec := range map[string]string{
		"missing file": "/nonexistent/sa.json",
		"bad json":     "{nope",
		"no key":       `{"client_email":"a"}`,
		"bad pem":      `{"client_email":"a","private_key":"x"}`,
	} {
		if _, err := Load(spec); err == nil {
			t.Errorf("%s: expected error", name)
		}
	}
}
