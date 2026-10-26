package router

import (
	"sort"
	"sync"
	"time"

	"gorouter/internal/config"
)

// Quotas tracks fixed-window request/token usage per account. A window starts at the
// first use after the previous one ended, mirroring subscription-style limits.
// State is in memory: it resets on restart.
type Quotas struct {
	mu    sync.Mutex
	now   func() time.Time
	m     map[string][]*window // account key -> one window per configured quota
	p     QuotaPersister
	dirty bool
}

// Window is the persisted form of one quota window. Dur guards against applying saved
// state after the configured window length changed.
type Window struct {
	Start    time.Time
	Dur      time.Duration
	Requests int
	Tokens   int
}

// QuotaPersister stores quota windows; implemented by the SQLite store.
type QuotaPersister interface {
	LoadQuotas() (map[string][]Window, error)
	SaveQuotas(map[string][]Window) error
}

type window struct {
	start    time.Time
	dur      time.Duration
	requests int
	tokens   int
}

func NewQuotas() *Quotas { return &Quotas{now: time.Now, m: map[string][]*window{}} }

// Attach loads saved windows from p and persists future changes through Flush.
func (q *Quotas) Attach(p QuotaPersister) error {
	saved, err := p.LoadQuotas()
	q.mu.Lock()
	defer q.mu.Unlock()
	q.p = p
	for k, ws := range saved {
		for _, w := range ws {
			q.m[k] = append(q.m[k], &window{start: w.Start, dur: w.Dur, requests: w.Requests, tokens: w.Tokens})
		}
	}
	return err
}

// Flush writes the windows to the persister if anything changed since the last flush.
func (q *Quotas) Flush() error {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.p == nil || !q.dirty {
		return nil
	}
	now := q.now()
	out := map[string][]Window{}
	for k, ws := range q.m {
		live := false
		var row []Window
		for _, w := range ws {
			row = append(row, Window{w.start, w.dur, w.requests, w.tokens})
			if !w.start.IsZero() && now.Before(w.start.Add(w.dur)) {
				live = true
			}
		}
		if live { // expired accounts carry no information
			out[k] = row
		}
	}
	if err := q.p.SaveQuotas(out); err != nil {
		return err
	}
	q.dirty = false
	return nil
}

// windows returns one window per configured quota, resetting any whose length changed.
func (q *Quotas) windows(key string, qs []config.Quota) []*window {
	w := q.m[key]
	for len(w) < len(qs) {
		w = append(w, &window{})
	}
	w = w[:len(qs)]
	for i, qc := range qs {
		if w[i].dur != qc.Duration {
			*w[i] = window{dur: qc.Duration}
		}
	}
	q.m[key] = w
	return w
}

func (w *window) expired(now time.Time, d time.Duration) bool {
	return w.start.IsZero() || !now.Before(w.start.Add(d))
}

// Allowed reports whether the account is under every configured limit.
func (q *Quotas) Allowed(key string, qs []config.Quota) bool {
	if len(qs) == 0 {
		return true
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	now := q.now()
	for i, w := range q.windows(key, qs) {
		if w.expired(now, qs[i].Duration) {
			continue
		}
		if (qs[i].MaxRequests > 0 && w.requests >= qs[i].MaxRequests) || (qs[i].MaxTokens > 0 && w.tokens >= qs[i].MaxTokens) {
			return false
		}
	}
	return true
}

// Add records usage against every window of the account.
func (q *Quotas) Add(key string, qs []config.Quota, requests, tokens int) {
	if len(qs) == 0 {
		return
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	now := q.now()
	for i, w := range q.windows(key, qs) {
		if w.expired(now, qs[i].Duration) {
			*w = window{start: now, dur: qs[i].Duration}
		}
		w.requests += requests
		w.tokens += tokens
		q.dirty = true
	}
}

type QuotaStatus struct {
	Account     string    `json:"account"`
	Window      string    `json:"window"`
	Requests    int       `json:"requests"`
	MaxRequests int       `json:"max_requests"`
	Tokens      int       `json:"tokens"`
	MaxTokens   int       `json:"max_tokens"`
	ResetsAt    time.Time `json:"resets_at"`
	Exhausted   bool      `json:"exhausted"`
}

// Snapshot lists the current state of every quota window for the given providers.
func (q *Quotas) Snapshot(r *Router) []QuotaStatus {
	q.mu.Lock()
	defer q.mu.Unlock()
	now := q.now()
	var out []QuotaStatus
	for _, id := range r.order {
		p := r.providers[id]
		for _, a := range p.Accounts {
			key := id + "#" + a.ID
			ws := q.windows(key, p.Quotas)
			for i, qc := range p.Quotas {
				st := QuotaStatus{Account: key, Window: qc.Window, MaxRequests: qc.MaxRequests, MaxTokens: qc.MaxTokens}
				if w := ws[i]; !w.expired(now, qc.Duration) {
					st.Requests, st.Tokens, st.ResetsAt = w.requests, w.tokens, w.start.Add(qc.Duration)
					st.Exhausted = (qc.MaxRequests > 0 && w.requests >= qc.MaxRequests) || (qc.MaxTokens > 0 && w.tokens >= qc.MaxTokens)
				}
				out = append(out, st)
			}
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Account+out[i].Window < out[j].Account+out[j].Window })
	return out
}
