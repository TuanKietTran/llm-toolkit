package router

import (
	"testing"
	"time"

	"gorouter/internal/config"
)

type memQuotaStore struct{ m map[string][]Window }

func (s *memQuotaStore) LoadQuotas() (map[string][]Window, error) { return s.m, nil }
func (s *memQuotaStore) SaveQuotas(m map[string][]Window) error   { s.m = m; return nil }

func TestQuotaStatePersistsAcrossRestart(t *testing.T) {
	qs := []config.Quota{{Window: "1h", Duration: time.Hour, MaxRequests: 2}}
	store := &memQuotaStore{}
	now := time.Now()

	a := NewQuotas()
	a.now = func() time.Time { return now }
	if err := a.Attach(store); err != nil {
		t.Fatal(err)
	}
	a.Add("p#a", qs, 1, 10)
	a.Add("p#a", qs, 1, 10)
	if a.Allowed("p#a", qs) {
		t.Fatal("should be exhausted after 2 requests")
	}
	if err := a.Flush(); err != nil {
		t.Fatal(err)
	}

	// "Restart": fresh Quotas loading the saved state 10 minutes later.
	b := NewQuotas()
	b.now = func() time.Time { return now.Add(10 * time.Minute) }
	if err := b.Attach(store); err != nil {
		t.Fatal(err)
	}
	if b.Allowed("p#a", qs) {
		t.Fatal("exhausted state must survive restart")
	}
	// After the window ends the account is usable again.
	b.now = func() time.Time { return now.Add(61 * time.Minute) }
	if !b.Allowed("p#a", qs) {
		t.Fatal("window should have reset")
	}
}

func TestQuotaWindowLengthChangeResets(t *testing.T) {
	store := &memQuotaStore{}
	a := NewQuotas()
	a.Attach(store)
	a.Add("p#a", []config.Quota{{Duration: time.Hour, MaxRequests: 1}}, 1, 0)
	a.Flush()

	b := NewQuotas()
	b.Attach(store)
	changed := []config.Quota{{Duration: 24 * time.Hour, MaxRequests: 1}}
	if !b.Allowed("p#a", changed) {
		t.Fatal("state saved for a different window length must not apply")
	}
}

func TestFlushSkipsWhenClean(t *testing.T) {
	store := &memQuotaStore{}
	q := NewQuotas()
	q.Attach(store)
	q.Flush()
	if store.m != nil {
		t.Fatal("nothing changed, nothing should be written")
	}
}
