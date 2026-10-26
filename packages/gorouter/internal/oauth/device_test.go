package oauth

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"gorouter/internal/config"
)

type memStore struct {
	mu sync.Mutex
	m  map[string]Token
}

func (s *memStore) GetToken(k string) (Token, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	t, ok := s.m[k]
	return t, ok, nil
}
func (s *memStore) SaveToken(k string, t Token) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.m[k] = t
	return nil
}
func (s *memStore) DeleteToken(k string) error { delete(s.m, k); return nil }

func TestDeviceFlow(t *testing.T) {
	var polls atomic.Int32
	idp := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.ParseForm()
		switch r.URL.Path {
		case "/device":
			io.WriteString(w, `{"device_code":"dc","user_code":"ABCD-1234","verification_uri":"https://idp/activate","expires_in":60,"interval":1}`)
		case "/token":
			if r.Form.Get("device_code") != "dc" {
				w.WriteHeader(400)
				return
			}
			// GitHub-style: errors come back as 200 + JSON.
			if polls.Add(1) < 3 {
				io.WriteString(w, `{"error":"authorization_pending"}`)
				return
			}
			io.WriteString(w, `{"access_token":"at","refresh_token":"rt","expires_in":3600}`)
		}
	}))
	defer idp.Close()

	st := &memStore{m: map[string]Token{}}
	m := NewManager(st)
	m.pollOverride = 10 * time.Millisecond
	p := config.Provider{ID: "gh", OAuth: &config.OAuth{ClientID: "c", DeviceURL: idp.URL + "/device", TokenURL: idp.URL + "/token"}}

	flow, err := m.StartDevice(context.Background(), p, "work")
	if err != nil || flow.UserCode != "ABCD-1234" {
		t.Fatalf("flow=%+v err=%v", flow, err)
	}
	if s := m.DeviceStatus("gh", "work"); s.State != "pending" {
		t.Fatalf("status %+v", s)
	}
	deadline := time.Now().Add(3 * time.Second)
	for m.DeviceStatus("gh", "work").State != "connected" {
		if time.Now().After(deadline) {
			t.Fatalf("never connected: %+v", m.DeviceStatus("gh", "work"))
		}
		time.Sleep(10 * time.Millisecond)
	}
	if tok, ok, _ := st.GetToken("gh#work"); !ok || tok.AccessToken != "at" || tok.RefreshToken != "rt" {
		t.Fatalf("stored %+v", tok)
	}
}

func TestDeviceFlowDenied(t *testing.T) {
	idp := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/device" {
			io.WriteString(w, `{"device_code":"dc","user_code":"U","verification_uri":"v","expires_in":60,"interval":1}`)
			return
		}
		w.WriteHeader(400)
		io.WriteString(w, `{"error":"access_denied"}`)
	}))
	defer idp.Close()
	m := NewManager(&memStore{m: map[string]Token{}})
	m.pollOverride = 10 * time.Millisecond
	p := config.Provider{ID: "x", OAuth: &config.OAuth{ClientID: "c", DeviceURL: idp.URL + "/device", TokenURL: idp.URL + "/token"}}
	if _, err := m.StartDevice(context.Background(), p, ""); err != nil {
		t.Fatal(err)
	}
	time.Sleep(200 * time.Millisecond)
	if s := m.DeviceStatus("x", ""); s.State != "error" || s.Error != "access_denied" {
		t.Fatalf("status %+v", s)
	}
}
