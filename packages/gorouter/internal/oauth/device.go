package oauth

import (
	"context"
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

// DeviceFlow is what the user needs to complete a device-code login.
type DeviceFlow struct {
	UserCode                string `json:"user_code"`
	VerificationURI         string `json:"verification_uri"`
	VerificationURIComplete string `json:"verification_uri_complete,omitempty"`
	ExpiresIn               int    `json:"expires_in"`
	Interval                int    `json:"interval"`
}

type DeviceStatus struct {
	State string `json:"state"` // "", "pending", "connected", "error"
	Error string `json:"error,omitempty"`
}

type deviceState struct {
	mu sync.Mutex
	st DeviceStatus
}

func (d *deviceState) set(state, msg string) {
	d.mu.Lock()
	d.st = DeviceStatus{state, msg}
	d.mu.Unlock()
}

// StartDevice begins the RFC 8628 device authorization flow and polls in the background
// until the user approves, denies, or the code expires.
func (m *Manager) StartDevice(ctx context.Context, p config.Provider, account string) (DeviceFlow, error) {
	o := p.OAuth
	if o == nil || o.DeviceURL == "" {
		return DeviceFlow{}, fmt.Errorf("provider %q has no oauth.device_url", p.ID)
	}
	form := url.Values{"client_id": {o.ClientID}}
	if len(o.Scopes) > 0 {
		form.Set("scope", strings.Join(o.Scopes, " "))
	}
	var resp struct {
		DeviceCode              string `json:"device_code"`
		UserCode                string `json:"user_code"`
		VerificationURI         string `json:"verification_uri"`
		VerificationURL         string `json:"verification_url"` // Google spells it this way
		VerificationURIComplete string `json:"verification_uri_complete"`
		ExpiresIn               int    `json:"expires_in"`
		Interval                int    `json:"interval"`
	}
	status, body, err := m.postForm(ctx, o.DeviceURL, form)
	if err != nil {
		return DeviceFlow{}, err
	}
	if status/100 != 2 || json.Unmarshal(body, &resp) != nil || resp.DeviceCode == "" {
		return DeviceFlow{}, fmt.Errorf("device endpoint %d: %s", status, strings.TrimSpace(string(body)))
	}
	if resp.VerificationURI == "" {
		resp.VerificationURI = resp.VerificationURL
	}
	if resp.Interval <= 0 {
		resp.Interval = 5
	}
	if resp.ExpiresIn <= 0 {
		resp.ExpiresIn = 900
	}

	key := TokenKey(p.ID, account)
	ds := &deviceState{st: DeviceStatus{State: "pending"}}
	m.mu.Lock()
	if m.devices == nil {
		m.devices = map[string]*deviceState{}
	}
	m.devices[key] = ds // a new attempt supersedes any previous one
	m.mu.Unlock()

	go m.pollDevice(p, key, resp.DeviceCode, time.Duration(resp.Interval)*time.Second, time.Duration(resp.ExpiresIn)*time.Second, ds)
	return DeviceFlow{resp.UserCode, resp.VerificationURI, resp.VerificationURIComplete, resp.ExpiresIn, resp.Interval}, nil
}

func (m *Manager) DeviceStatus(providerID, account string) DeviceStatus {
	m.mu.Lock()
	ds := m.devices[TokenKey(providerID, account)]
	m.mu.Unlock()
	if ds == nil {
		return DeviceStatus{}
	}
	ds.mu.Lock()
	defer ds.mu.Unlock()
	return ds.st
}

func (m *Manager) pollDevice(p config.Provider, key, deviceCode string, interval, ttl time.Duration, ds *deviceState) {
	if m.pollOverride > 0 {
		interval = m.pollOverride
	}
	ctx, cancel := context.WithTimeout(context.Background(), ttl)
	defer cancel()
	for {
		select {
		case <-ctx.Done():
			ds.set("error", "device code expired")
			return
		case <-time.After(interval):
		}
		m.mu.Lock()
		superseded := m.devices[key] != ds
		m.mu.Unlock()
		if superseded {
			return
		}
		form := url.Values{
			"grant_type":  {"urn:ietf:params:oauth:grant-type:device_code"},
			"device_code": {deviceCode},
			"client_id":   {p.OAuth.ClientID},
		}
		if p.OAuth.ClientSecret != "" {
			form.Set("client_secret", p.OAuth.ClientSecret)
		}
		_, body, err := m.postForm(ctx, p.OAuth.TokenURL, form)
		if err != nil {
			if errors.Is(err, context.DeadlineExceeded) {
				continue
			}
			ds.set("error", err.Error())
			return
		}
		var tr struct {
			AccessToken  string `json:"access_token"`
			RefreshToken string `json:"refresh_token"`
			ExpiresIn    int64  `json:"expires_in"`
			Error        string `json:"error"`
			Description  string `json:"error_description"`
		}
		_ = json.Unmarshal(body, &tr)
		switch {
		case tr.AccessToken != "":
			t := Token{AccessToken: tr.AccessToken, RefreshToken: tr.RefreshToken}
			if tr.ExpiresIn > 0 {
				t.Expiry = time.Now().Add(time.Duration(tr.ExpiresIn) * time.Second)
			}
			if err := m.store.SaveToken(key, t); err != nil {
				ds.set("error", err.Error())
			} else {
				ds.set("connected", "")
			}
			return
		case tr.Error == "authorization_pending" || tr.Error == "":
		case tr.Error == "slow_down":
			interval += 5 * time.Second
		default:
			ds.set("error", strings.TrimSpace(tr.Error+" "+tr.Description))
			return
		}
	}
}

func (m *Manager) postForm(ctx context.Context, u string, form url.Values) (int, []byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, strings.NewReader(form.Encode()))
	if err != nil {
		return 0, nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	resp, err := m.client.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	return resp.StatusCode, b, err
}
