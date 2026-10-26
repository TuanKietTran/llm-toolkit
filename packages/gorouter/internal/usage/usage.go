// Package usage tracks per-provider/model request counts, tokens, errors and estimated cost.
package usage

import (
	"sync"
	"time"
)

type Stat struct {
	Requests   int64     `json:"requests"`
	Errors     int64     `json:"errors"`
	InTokens   int64     `json:"in_tokens"`
	OutTokens  int64     `json:"out_tokens"`
	CostUSD    float64   `json:"cost_usd"`
	LatencyMs  int64     `json:"total_latency_ms"`
	LastUsedAt time.Time `json:"last_used_at"`
}

// Persister loads and saves stats; implemented by the SQLite store.
type Persister interface {
	LoadUsage() (map[string]*Stat, error)
	SaveUsage(map[string]*Stat) error
}

type Tracker struct {
	mu    sync.Mutex
	p     Persister
	dirty bool
	Stats map[string]*Stat
}

// New loads existing stats from p. A nil Persister keeps stats in memory only.
func New(p Persister) *Tracker {
	t := &Tracker{p: p, Stats: map[string]*Stat{}}
	if p != nil {
		if m, err := p.LoadUsage(); err == nil && m != nil {
			t.Stats = m
		}
	}
	return t
}

type Record struct {
	Key               string
	In, Out           int
	PriceIn, PriceOut float64
	Latency           time.Duration
	Err               bool
}

func (t *Tracker) Add(r Record) {
	t.mu.Lock()
	defer t.mu.Unlock()
	s := t.Stats[r.Key]
	if s == nil {
		s = &Stat{}
		t.Stats[r.Key] = s
	}
	s.Requests++
	if r.Err {
		s.Errors++
	}
	s.InTokens += int64(r.In)
	s.OutTokens += int64(r.Out)
	s.CostUSD += float64(r.In)/1e6*r.PriceIn + float64(r.Out)/1e6*r.PriceOut
	s.LatencyMs += r.Latency.Milliseconds()
	s.LastUsedAt = time.Now()
	t.dirty = true
}

func (t *Tracker) Snapshot() map[string]Stat {
	t.mu.Lock()
	defer t.mu.Unlock()
	out := make(map[string]Stat, len(t.Stats))
	for k, v := range t.Stats {
		out[k] = *v
	}
	return out
}

// Flush saves stats if changed.
func (t *Tracker) Flush() error {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.p == nil || !t.dirty {
		return nil
	}
	if err := t.p.SaveUsage(t.Stats); err != nil {
		return err
	}
	t.dirty = false
	return nil
}

// Run flushes periodically until stop is closed.
func (t *Tracker) Run(stop <-chan struct{}) {
	tick := time.NewTicker(10 * time.Second)
	defer tick.Stop()
	for {
		select {
		case <-tick.C:
			_ = t.Flush()
		case <-stop:
			_ = t.Flush()
			return
		}
	}
}
