// Package oauth implements the OAuth 2.0 authorization-code + PKCE flow with
// automatic access-token refresh for providers configured with an `oauth` block.
package oauth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"gorouter/internal/config"
)

const (
	refreshSkew = 60 * time.Second
	pendingTTL  = 10 * time.Minute
)

type Token struct {
	AccessToken  string
	RefreshToken string
	Expiry       time.Time // zero = does not expire
}

func (t Token) valid() bool {
	return t.AccessToken != "" && (t.Expiry.IsZero() || time.Now().Add(refreshSkew).Before(t.Expiry))
}

type TokenStore interface {
	GetToken(provider string) (Token, bool, error)
	SaveToken(provider string, t Token) error
	DeleteToken(provider string) error
}

type pending struct {
	provider    config.Provider
	account     string
	verifier    string
	redirectURI string
	created     time.Time
}

type Manager struct {
	store  TokenStore
	client *http.Client

	mu      sync.Mutex
	pending map[string]pending
	devices map[string]*deviceState

	pollOverride time.Duration          // tests only
	locks        map[string]*sync.Mutex // serialises refreshes per provider
}

func NewManager(store TokenStore) *Manager {
	return &Manager{
		store:   store,
		client:  &http.Client{Timeout: 30 * time.Second},
		pending: map[string]pending{},
		locks:   map[string]*sync.Mutex{},
	}
}

func randString(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}

// Start begins an authorization flow and returns the URL the user must visit.
func (m *Manager) Start(p config.Provider, account, redirectURI string) (string, error) {
	if p.OAuth == nil {
		return "", fmt.Errorf("provider %q has no oauth config", p.ID)
	}
	verifier := randString(48)
	sum := sha256.Sum256([]byte(verifier))
	state := randString(24)

	m.mu.Lock()
	for k, v := range m.pending { // drop expired attempts
		if time.Since(v.created) > pendingTTL {
			delete(m.pending, k)
		}
	}
	m.pending[state] = pending{p, account, verifier, redirectURI, time.Now()}
	m.mu.Unlock()

	u, err := url.Parse(p.OAuth.AuthURL)
	if err != nil {
		return "", err
	}
	q := u.Query()
	q.Set("response_type", "code")
	q.Set("client_id", p.OAuth.ClientID)
	q.Set("redirect_uri", redirectURI)
	q.Set("state", state)
	q.Set("code_challenge", base64.RawURLEncoding.EncodeToString(sum[:]))
	q.Set("code_challenge_method", "S256")
	if len(p.OAuth.Scopes) > 0 {
		q.Set("scope", strings.Join(p.OAuth.Scopes, " "))
	}
	for k, v := range p.OAuth.ExtraParams {
		q.Set(k, v)
	}
	u.RawQuery = q.Encode()
	return u.String(), nil
}

// TokenKey is the storage key for a provider account. The implicit default account uses the bare provider id.
func TokenKey(providerID, account string) string {
	if account == "" || account == config.DefaultAccount {
		return providerID
	}
	return providerID + "#" + account
}

// Callback completes the flow and stores the tokens. It returns the provider id and account.
func (m *Manager) Callback(ctx context.Context, state, code string) (string, error) {
	m.mu.Lock()
	pd, ok := m.pending[state]
	delete(m.pending, state)
	m.mu.Unlock()
	if !ok || time.Since(pd.created) > pendingTTL {
		return "", errors.New("unknown or expired oauth state")
	}
	tok, err := m.tokenRequest(ctx, pd.provider.OAuth, url.Values{
		"grant_type":    {"authorization_code"},
		"code":          {code},
		"redirect_uri":  {pd.redirectURI},
		"code_verifier": {pd.verifier},
	}, "")
	if err != nil {
		return "", err
	}
	return TokenKey(pd.provider.ID, pd.account), m.store.SaveToken(TokenKey(pd.provider.ID, pd.account), tok)
}

// Token returns a valid access token for p, refreshing it when close to expiry.
func (m *Manager) Token(ctx context.Context, p config.Provider, account string) (string, error) {
	if p.OAuth == nil {
		return "", nil
	}
	key := TokenKey(p.ID, account)
	lock := m.lockFor(key)
	lock.Lock()
	defer lock.Unlock()

	t, ok, err := m.store.GetToken(key)
	if err != nil {
		return "", err
	}
	if !ok {
		return "", fmt.Errorf("%q is not connected: complete the OAuth login first", key)
	}
	if t.valid() {
		return t.AccessToken, nil
	}
	if t.RefreshToken == "" {
		return "", fmt.Errorf("%q: access token expired and no refresh token; reconnect", key)
	}
	nt, err := m.tokenRequest(ctx, p.OAuth, url.Values{
		"grant_type":    {"refresh_token"},
		"refresh_token": {t.RefreshToken},
	}, t.RefreshToken)
	if err != nil {
		return "", fmt.Errorf("refresh %q: %w", key, err)
	}
	if err := m.store.SaveToken(key, nt); err != nil {
		return "", err
	}
	return nt.AccessToken, nil
}

// Status reports whether a provider currently has stored credentials and when they expire.
func (m *Manager) Status(providerID, account string) (connected bool, expiry time.Time) {
	t, ok, _ := m.store.GetToken(TokenKey(providerID, account))
	return ok, t.Expiry
}

func (m *Manager) Disconnect(providerID, account string) error {
	return m.store.DeleteToken(TokenKey(providerID, account))
}

func (m *Manager) lockFor(id string) *sync.Mutex {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.locks[id] == nil {
		m.locks[id] = &sync.Mutex{}
	}
	return m.locks[id]
}

// tokenRequest posts to the token endpoint. keepRefresh is retained when the
// server does not rotate the refresh token.
func (m *Manager) tokenRequest(ctx context.Context, o *config.OAuth, form url.Values, keepRefresh string) (Token, error) {
	form.Set("client_id", o.ClientID)
	if o.ClientSecret != "" {
		form.Set("client_secret", o.ClientSecret)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, o.TokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return Token{}, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	resp, err := m.client.Do(req)
	if err != nil {
		return Token{}, err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode/100 != 2 {
		return Token{}, fmt.Errorf("token endpoint %d: %s", resp.StatusCode, strings.TrimSpace(string(b)))
	}
	var tr struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		ExpiresIn    int64  `json:"expires_in"`
	}
	if err := json.Unmarshal(b, &tr); err != nil || tr.AccessToken == "" {
		return Token{}, fmt.Errorf("token endpoint returned no access_token: %s", strings.TrimSpace(string(b)))
	}
	t := Token{AccessToken: tr.AccessToken, RefreshToken: tr.RefreshToken}
	if t.RefreshToken == "" {
		t.RefreshToken = keepRefresh
	}
	if tr.ExpiresIn > 0 {
		t.Expiry = time.Now().Add(time.Duration(tr.ExpiresIn) * time.Second)
	}
	return t, nil
}
