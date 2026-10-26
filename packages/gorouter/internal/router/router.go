// Package router resolves model names (aliases, combos, provider/model) into
// ordered targets and executes requests with fallback.
package router

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"gorouter/internal/config"
	"gorouter/internal/gcpauth"
	"gorouter/internal/provider"
)

const maxDepth = 8

type Target struct {
	Provider *provider.Provider
	Model    string
	Account  string // set once accounts are expanded
}

// String is the provider/model route, used for usage stats and de-duplication.
func (t Target) String() string { return t.Provider.ID + "/" + t.Model }

// AccountKey identifies the credential set for cooldowns and quotas.
func (t Target) AccountKey() string { return t.Provider.ID + "#" + t.Account }
func (t Target) ModelKey() string   { return t.AccountKey() + "/" + t.Model }

// Label is the route plus account, for logs and attempts.
func (t Target) Label() string {
	if t.Account == "" || t.Account == config.DefaultAccount {
		return t.String()
	}
	return t.String() + "@" + t.Account
}

// TokenSource supplies OAuth access tokens for providers configured with oauth.
type TokenSource interface {
	Token(ctx context.Context, p config.Provider, account string) (string, error)
}

type Router struct {
	tokens    TokenSource
	providers map[string]*provider.Provider
	order     []string
	combos    map[string]config.Combo
	aliases   map[string]string
	Health    *Health
	Quota     *Quotas
	GCP       *gcpauth.Registry
	Client    *http.Client

	mu sync.Mutex
	rr map[string]int
}

// New builds a router. A non-nil prev lends its Health so cooldowns survive config reloads.
func New(cfg *config.Config, tokens TokenSource, prev *Router) *Router {
	r := &Router{
		tokens:    tokens,
		providers: map[string]*provider.Provider{},
		combos:    map[string]config.Combo{},
		aliases:   cfg.Aliases,
		rr:        map[string]int{},
		Quota:     NewQuotas(),
		GCP:       gcpauth.NewRegistry(),
		Health:    NewHealth(time.Duration(cfg.Cooldown.BaseSec)*time.Second, time.Duration(cfg.Cooldown.MaxSec)*time.Second),
		Client: &http.Client{Transport: &http.Transport{
			ResponseHeaderTimeout: 120 * time.Second,
			MaxIdleConnsPerHost:   32,
			IdleConnTimeout:       90 * time.Second,
		}},
	}
	if prev != nil {
		r.Health = prev.Health
		r.Quota = prev.Quota
		r.GCP = prev.GCP
		r.Client = prev.Client
	}
	for _, pc := range cfg.Providers {
		if pc.Disabled {
			continue
		}
		r.providers[pc.ID] = provider.New(pc)
		r.order = append(r.order, pc.ID)
	}
	for _, c := range cfg.Combos {
		r.combos[c.Name] = c
	}
	return r
}

// Resolve expands a requested model into an ordered, de-duplicated target list.
func (r *Router) Resolve(model string) ([]Target, error) {
	ts, err := r.resolve(model, 0)
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	var out []Target
	for _, t := range ts {
		if !seen[t.String()] {
			seen[t.String()] = true
			out = append(out, t)
		}
	}
	return out, nil
}

func (r *Router) resolve(name string, depth int) ([]Target, error) {
	if depth > maxDepth {
		return nil, fmt.Errorf("model %q: alias/combo nesting too deep", name)
	}
	if a, ok := r.aliases[name]; ok {
		return r.resolve(a, depth+1)
	}
	if c, ok := r.combos[name]; ok {
		var all [][]Target
		for _, m := range c.Models {
			ts, err := r.resolve(m, depth+1)
			if err != nil {
				return nil, fmt.Errorf("combo %q: %w", name, err)
			}
			all = append(all, ts)
		}
		switch c.Strategy {
		case "round_robin":
			r.mu.Lock()
			start := r.rr[name] % len(all)
			r.rr[name]++
			r.mu.Unlock()
			all = append(all[start:], all[:start]...)
		}
		var flat []Target
		for _, ts := range all {
			flat = append(flat, ts...)
		}
		if c.Strategy == "tier" {
			sort.SliceStable(flat, func(i, j int) bool { return flat[i].Provider.Tier < flat[j].Provider.Tier })
		}
		return flat, nil
	}
	if pid, m, ok := strings.Cut(name, "/"); ok {
		p := r.providers[pid]
		if p == nil {
			return nil, fmt.Errorf("unknown provider %q in model %q", pid, name)
		}
		return []Target{{Provider: p, Model: m}}, nil
	}
	// Bare model name: match providers that list it.
	var ts []Target
	for _, id := range r.order {
		for _, m := range r.providers[id].Models {
			if m == name {
				ts = append(ts, Target{Provider: r.providers[id], Model: m})
			}
		}
	}
	if len(ts) == 0 {
		return nil, fmt.Errorf("unknown model %q", name)
	}
	return ts, nil
}

type Attempt struct {
	Target string `json:"target"`
	Status int    `json:"status"`
	Error  string `json:"error"`
}

type Result struct {
	Resp     *http.Response
	Target   Target
	Attempts []Attempt
}

// Failure is returned when no target produced a successful response.
type Failure struct {
	Status   int
	Message  string
	Attempts []Attempt
}

func (f *Failure) Error() string { return f.Message }

// Execute sends an OpenAI-format chat body. The caller owns Result.Resp.Body.
func (r *Router) Execute(ctx context.Context, body map[string]any, model string) (*Result, *Failure) {
	return r.Run(ctx, model, nil, func(t Target, p *provider.Provider, secret string) (*http.Request, error) {
		return p.BuildRequest(ctx, body, t.Model, secret)
	})
}

// BuildFunc creates the upstream request for one target. p carries the account's api_key;
// a non-empty secret is an OAuth access token.
type BuildFunc func(t Target, p *provider.Provider, secret string) (*http.Request, error)

// Run resolves model and tries each target/account in order until one answers 2xx.
// accept, if non-nil, filters out providers that cannot serve the request.
func (r *Router) Run(ctx context.Context, model string, accept func(*provider.Provider) bool, build BuildFunc) (*Result, *Failure) {
	resolved, err := r.Resolve(model)
	if err != nil {
		return nil, &Failure{Status: http.StatusNotFound, Message: err.Error()}
	}
	var targets []Target
	for _, t := range resolved {
		if accept != nil && !accept(t.Provider) {
			continue
		}
		targets = append(targets, r.expandAccounts(t)...)
	}
	if len(targets) == 0 {
		return nil, &Failure{Status: http.StatusBadRequest, Message: "no provider for model " + model + " supports this request"}
	}
	// Prefer healthy, in-quota accounts; if none qualify, fail open and try everything.
	var avail []Target
	for _, t := range targets {
		if r.Health.Available(t.AccountKey(), t.ModelKey()) && r.Quota.Allowed(t.AccountKey(), t.Provider.Quotas) {
			avail = append(avail, t)
		}
	}
	if len(avail) == 0 {
		avail = targets
	}

	var attempts []Attempt
	lastStatus := http.StatusBadGateway
	for _, t := range avail {
		p, secret, terr := r.credentials(ctx, t)
		if terr != nil {
			r.Health.Fail(t.AccountKey(), 0)
			attempts = append(attempts, Attempt{t.Label(), 0, terr.Error()})
			lastStatus = http.StatusUnauthorized
			continue
		}
		req, err := build(t, p, secret)
		if err != nil {
			attempts = append(attempts, Attempt{t.Label(), 0, err.Error()})
			continue
		}
		resp, err := r.Client.Do(req)
		if err != nil {
			if ctx.Err() != nil {
				return nil, &Failure{Status: 499, Message: "client closed request", Attempts: attempts}
			}
			r.Health.Fail(t.ModelKey(), 0)
			attempts = append(attempts, Attempt{t.Label(), 0, err.Error()})
			lastStatus = http.StatusBadGateway
			continue
		}
		if resp.StatusCode/100 == 2 {
			r.Health.Success(t.AccountKey(), t.ModelKey())
			r.Quota.Add(t.AccountKey(), t.Provider.Quotas, 1, 0)
			attempts = append(attempts, Attempt{t.Label(), resp.StatusCode, ""})
			return &Result{Resp: resp, Target: t, Attempts: attempts}, nil
		}
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
		resp.Body.Close()
		msg := strings.TrimSpace(string(b))
		attempts = append(attempts, Attempt{t.Label(), resp.StatusCode, msg})
		if !retryable(resp.StatusCode) {
			return nil, &Failure{Status: resp.StatusCode, Message: msg, Attempts: attempts}
		}
		lastStatus = resp.StatusCode
		ra := parseRetryAfter(resp.Header.Get("Retry-After"))
		switch resp.StatusCode {
		case http.StatusTooManyRequests, http.StatusUnauthorized, http.StatusForbidden:
			r.Health.Fail(t.AccountKey(), ra) // account-level problem
		default:
			r.Health.Fail(t.ModelKey(), ra)
		}
	}
	if len(attempts) == 0 {
		return nil, &Failure{Status: http.StatusServiceUnavailable, Message: "no targets available"}
	}
	status := lastStatus
	if status < 500 && status != http.StatusTooManyRequests {
		status = http.StatusBadGateway
	}
	return nil, &Failure{Status: status, Message: "all upstream targets failed: " + attempts[len(attempts)-1].Error, Attempts: attempts}
}

// expandAccounts returns one target per enabled account, ordered by the provider's account strategy.
func (r *Router) expandAccounts(t Target) []Target {
	var accts []config.Account
	for _, a := range t.Provider.Accounts {
		if !a.Disabled {
			accts = append(accts, a)
		}
	}
	if t.Provider.AccountStrategy == "round_robin" && len(accts) > 1 {
		r.mu.Lock()
		start := r.rr["acct:"+t.Provider.ID] % len(accts)
		r.rr["acct:"+t.Provider.ID]++
		r.mu.Unlock()
		accts = append(accts[start:], accts[:start]...)
	}
	out := make([]Target, 0, len(accts))
	for _, a := range accts {
		out = append(out, Target{Provider: t.Provider, Model: t.Model, Account: a.ID})
	}
	return out
}

// credentials returns a provider copy carrying the account's api_key, or an OAuth token.
func (r *Router) credentials(ctx context.Context, t Target) (*provider.Provider, string, error) {
	p := *t.Provider
	for _, a := range p.Accounts {
		if a.ID == t.Account {
			p.APIKey = a.APIKey
		}
	}
	for _, a := range p.Accounts {
		if a.ID == t.Account && a.ServiceAccount != "" {
			tok, err := r.GCP.Token(ctx, t.AccountKey(), a.ServiceAccount)
			return &p, tok, err
		}
	}
	if p.OAuth != nil && r.tokens != nil {
		tok, err := r.tokens.Token(ctx, p.Provider, t.Account)
		return &p, tok, err
	}
	return &p, "", nil
}

func retryable(status int) bool {
	switch status {
	case 401, 403, 408, 409, 425, 429:
		return true
	}
	return status >= 500
}

func parseRetryAfter(v string) time.Duration {
	if n, err := strconv.Atoi(strings.TrimSpace(v)); err == nil && n > 0 {
		return time.Duration(n) * time.Second
	}
	return 0
}

// Models lists every routable model name for /v1/models.
func (r *Router) Models() []string {
	var out []string
	for _, id := range r.order {
		for _, m := range r.providers[id].Models {
			out = append(out, id+"/"+m)
		}
	}
	for n := range r.combos {
		out = append(out, n)
	}
	for n := range r.aliases {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

func (r *Router) Providers() []*provider.Provider {
	var out []*provider.Provider
	for _, id := range r.order {
		out = append(out, r.providers[id])
	}
	return out
}
