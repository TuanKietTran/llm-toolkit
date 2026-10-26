// Package gcpauth mints Google OAuth2 access tokens from a service-account key
// using the JWT-bearer grant (RFC 7523), with in-memory caching.
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
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"
)

const (
	Scope       = "https://www.googleapis.com/auth/cloud-platform"
	defaultAud  = "https://oauth2.googleapis.com/token"
	refreshSkew = 60 * time.Second
)

type Key struct {
	ClientEmail string `json:"client_email"`
	PrivateKey  string `json:"private_key"`
	TokenURI    string `json:"token_uri"`
	rsa         *rsa.PrivateKey
}

// Load reads a service-account key. spec is raw JSON, "env:NAME" (holding JSON or a path), or a file path.
func Load(spec string) (*Key, error) {
	if name, ok := strings.CutPrefix(spec, "env:"); ok {
		spec = os.Getenv(name)
		if spec == "" {
			return nil, fmt.Errorf("environment variable %s is empty", name)
		}
	}
	data := []byte(spec)
	if !strings.HasPrefix(strings.TrimSpace(spec), "{") {
		b, err := os.ReadFile(spec)
		if err != nil {
			return nil, fmt.Errorf("service account: %w", err)
		}
		data = b
	}
	var k Key
	if err := json.Unmarshal(data, &k); err != nil {
		return nil, fmt.Errorf("service account json: %w", err)
	}
	if k.ClientEmail == "" || k.PrivateKey == "" {
		return nil, errors.New("service account json needs client_email and private_key")
	}
	if k.TokenURI == "" {
		k.TokenURI = defaultAud
	}
	block, _ := pem.Decode([]byte(k.PrivateKey))
	if block == nil {
		return nil, errors.New("service account private_key is not valid PEM")
	}
	parsed, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		if pk, err2 := x509.ParsePKCS1PrivateKey(block.Bytes); err2 == nil {
			parsed, err = pk, nil
		}
	}
	rk, ok := parsed.(*rsa.PrivateKey)
	if err != nil || !ok {
		return nil, errors.New("service account private_key must be an RSA key")
	}
	k.rsa = rk
	return &k, nil
}

func b64(b []byte) string { return base64.RawURLEncoding.EncodeToString(b) }

// assertion builds the signed JWT.
func (k *Key) assertion(now time.Time) (string, error) {
	hdr, _ := json.Marshal(map[string]string{"alg": "RS256", "typ": "JWT"})
	claims, _ := json.Marshal(map[string]any{
		"iss": k.ClientEmail, "scope": Scope, "aud": k.TokenURI,
		"iat": now.Unix(), "exp": now.Add(time.Hour).Unix(),
	})
	signing := b64(hdr) + "." + b64(claims)
	sum := sha256.Sum256([]byte(signing))
	sig, err := rsa.SignPKCS1v15(rand.Reader, k.rsa, crypto.SHA256, sum[:])
	if err != nil {
		return "", err
	}
	return signing + "." + b64(sig), nil
}

// Source caches and refreshes an access token for one key.
type Source struct {
	key    *Key
	client *http.Client
	mu     sync.Mutex
	tok    string
	exp    time.Time
}

func NewSource(k *Key) *Source {
	return &Source{key: k, client: &http.Client{Timeout: 30 * time.Second}}
}

func (s *Source) Token(ctx context.Context) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.tok != "" && time.Now().Add(refreshSkew).Before(s.exp) {
		return s.tok, nil
	}
	jwt, err := s.key.assertion(time.Now())
	if err != nil {
		return "", err
	}
	form := url.Values{"grant_type": {"urn:ietf:params:oauth:grant-type:jwt-bearer"}, "assertion": {jwt}}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.key.TokenURI, strings.NewReader(form.Encode()))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := s.client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	var tr struct {
		AccessToken string `json:"access_token"`
		ExpiresIn   int64  `json:"expires_in"`
	}
	if resp.StatusCode/100 != 2 || json.Unmarshal(b, &tr) != nil || tr.AccessToken == "" {
		return "", fmt.Errorf("token endpoint %d: %s", resp.StatusCode, strings.TrimSpace(string(b)))
	}
	if tr.ExpiresIn <= 0 {
		tr.ExpiresIn = 3600
	}
	s.tok, s.exp = tr.AccessToken, time.Now().Add(time.Duration(tr.ExpiresIn)*time.Second)
	return s.tok, nil
}

// Registry hands out one Source per account key and survives config reloads.
type Registry struct {
	mu sync.Mutex
	m  map[string]*entry
}

type entry struct {
	spec string
	src  *Source
}

func NewRegistry() *Registry { return &Registry{m: map[string]*entry{}} }

// Token returns a token for the account identified by key, creating the source on first use
// (or when the configured spec changed).
func (r *Registry) Token(ctx context.Context, key, spec string) (string, error) {
	r.mu.Lock()
	e := r.m[key]
	if e == nil || e.spec != spec {
		k, err := Load(spec)
		if err != nil {
			r.mu.Unlock()
			return "", err
		}
		e = &entry{spec: spec, src: NewSource(k)}
		r.m[key] = e
	}
	r.mu.Unlock()
	return e.src.Token(ctx)
}
