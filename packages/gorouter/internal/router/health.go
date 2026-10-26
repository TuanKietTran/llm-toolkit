package router

import (
	"sort"
	"sync"
	"time"
)

// Health tracks cooldowns with exponential backoff per key (provider or provider/model).
type Health struct {
	mu        sync.Mutex
	base, max time.Duration
	now       func() time.Time
	m         map[string]*hstate
}

type hstate struct {
	fails int
	until time.Time
}

func NewHealth(base, max time.Duration) *Health {
	return &Health{base: base, max: max, now: time.Now, m: map[string]*hstate{}}
}

func (h *Health) Available(keys ...string) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, k := range keys {
		if s := h.m[k]; s != nil && h.now().Before(s.until) {
			return false
		}
	}
	return true
}

// Fail puts key on cooldown. A positive retryAfter overrides the backoff.
func (h *Health) Fail(key string, retryAfter time.Duration) {
	h.mu.Lock()
	defer h.mu.Unlock()
	s := h.m[key]
	if s == nil {
		s = &hstate{}
		h.m[key] = s
	}
	s.fails++
	d := h.base << min(s.fails-1, 20)
	if d > h.max || d <= 0 {
		d = h.max
	}
	if retryAfter > 0 {
		d = min(retryAfter, h.max)
	}
	s.until = h.now().Add(d)
}

func (h *Health) Success(keys ...string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, k := range keys {
		delete(h.m, k)
	}
}

type Cooldown struct {
	Key       string    `json:"key"`
	Failures  int       `json:"failures"`
	Until     time.Time `json:"until"`
	RemainSec int       `json:"remaining_sec"`
}

func (h *Health) Snapshot() []Cooldown {
	h.mu.Lock()
	defer h.mu.Unlock()
	var out []Cooldown
	for k, s := range h.m {
		if rem := s.until.Sub(h.now()); rem > 0 {
			out = append(out, Cooldown{k, s.fails, s.until, int(rem.Seconds())})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	return out
}
