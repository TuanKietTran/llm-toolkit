// Package config loads and validates the router configuration file.
package config

import (
	"encoding/json"
	"fmt"
	"gorouter/internal/gcpauth"
	"os"
	"strings"
	"time"
)

type Config struct {
	Listen    string            `json:"listen"`
	DataDir   string            `json:"data_dir"`
	PublicURL string            `json:"public_url"` // base URL used for OAuth redirects, e.g. http://localhost:20128
	APIKeys   []string          `json:"api_keys"`
	Providers []Provider        `json:"providers"`
	Combos    []Combo           `json:"combos"`
	Aliases   map[string]string `json:"aliases"`
	Cooldown  Cooldown          `json:"cooldown"`
	Saver     TokenSaver        `json:"token_saver"`

	raw json.RawMessage // original document, kept for the admin API
}

// Raw returns the document the config was parsed from.
func (c *Config) Raw() json.RawMessage { return c.raw }
func (c *Config) SetRaw(b []byte)      { c.raw = append(json.RawMessage(nil), b...) }

type Provider struct {
	ID      string            `json:"id"`
	Type    string            `json:"type"` // "openai" | "anthropic"
	BaseURL string            `json:"base_url"`
	APIKey  string            `json:"api_key"` // literal or "env:NAME"
	Headers map[string]string `json:"headers"`
	Tier    int               `json:"tier"` // 1 = subscription, 2 = cheap, 3 = free (lower is tried first)
	Models  []string          `json:"models"`
	// Prices in USD per 1M tokens, used for cost estimation only.
	PriceIn  float64 `json:"price_in"`
	PriceOut float64 `json:"price_out"`
	// Disabled providers are never routed to.
	Disabled bool `json:"disabled"`
	// Accounts lists credentials to rotate over. Empty = one "default" account using api_key.
	// Vertex shorthand: set project (+ optional location, default us-central1) instead of base_url.
	Project         string    `json:"project"`
	Location        string    `json:"location"`
	ServiceAccount  string    `json:"service_account"` // shorthand for the default account
	Accounts        []Account `json:"accounts"`
	AccountStrategy string    `json:"account_strategy"` // "fallback" (default) | "round_robin"
	Quotas          []Quota   `json:"quotas"`           // applied to each account
	// OAuth, when set, makes the provider authenticate with a bearer token obtained
	// through the OAuth 2.0 authorization-code + PKCE flow instead of api_key.
	OAuth *OAuth `json:"oauth,omitempty"`
}

// Account is one credential set for a provider (e.g. a second subscription).
type Account struct {
	ID       string `json:"id"`
	APIKey   string `json:"api_key"` // literal or "env:NAME"; ignored when the provider uses oauth
	Disabled bool   `json:"disabled"`
	// ServiceAccount (vertex): Google service-account key as a file path, raw JSON or "env:NAME".
	ServiceAccount string `json:"service_account"`
}

// Quota limits usage of one account inside a fixed window that starts at first use.
type Quota struct {
	Window      string        `json:"window"` // Go duration, e.g. "5h", "24h", "168h"
	MaxRequests int           `json:"max_requests"`
	MaxTokens   int           `json:"max_tokens"`
	Duration    time.Duration `json:"-"`
}

type TokenSaver struct {
	Enabled            bool                   `json:"enabled"`
	MaxToolChars       int                    `json:"max_tool_chars"`    // default 8000
	TrimDiffContext    bool                   `json:"trim_diff_context"` // keep 1 line of context around diff changes
	TersePrompt        string                 `json:"terse_prompt"`      // appended to the system prompt when non-empty
	CacheControl       bool                   `json:"cache_control"`
	DedupeToolMessages bool                   `json:"dedupe_tool_messages"`
	MaxHistoryMessages int                    `json:"max_history_messages"`
	MaxPromptChars     int                    `json:"max_prompt_chars"`
	RouteBudgets       map[string]RouteBudget `json:"route_budgets"`
}

type RouteBudget struct {
	MaxToolChars       int `json:"max_tool_chars"`
	MaxHistoryMessages int `json:"max_history_messages"`
	MaxPromptChars     int `json:"max_prompt_chars"`
}

type OAuth struct {
	AuthURL      string            `json:"auth_url"`
	TokenURL     string            `json:"token_url"`
	ClientID     string            `json:"client_id"`
	ClientSecret string            `json:"client_secret"` // literal or "env:NAME"; optional for public clients
	DeviceURL    string            `json:"device_url"`    // device authorization endpoint; enables the device-code flow
	Scopes       []string          `json:"scopes"`
	ExtraParams  map[string]string `json:"extra_params"` // added to the authorization URL
}

type Combo struct {
	Name     string   `json:"name"`
	Strategy string   `json:"strategy"` // "fallback" (default) | "round_robin" | "tier"
	Models   []string `json:"models"`   // provider/model, alias or other combo
}

type Cooldown struct {
	BaseSec int `json:"base_sec"`
	MaxSec  int `json:"max_sec"`
}

func Load(path string) (*Config, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return Parse(b)
}

// Parse decodes, defaults and validates a JSON config document.
func Parse(b []byte) (*Config, error) {
	var c Config
	if err := json.Unmarshal(b, &c); err != nil {
		return nil, fmt.Errorf("parse config: %w", err)
	}
	c.ApplyDefaults()
	c.SetRaw(b)
	return &c, c.Validate()
}

// DefaultAccount is the id of the implicit account created from a provider's api_key.
const DefaultAccount = "default"

func (c *Config) ApplyDefaults() {
	if c.Listen == "" {
		c.Listen = ":20128"
	}
	if c.DataDir == "" {
		c.DataDir = "./data"
	}
	if c.Cooldown.BaseSec <= 0 {
		c.Cooldown.BaseSec = 5
	}
	if c.Cooldown.MaxSec <= 0 {
		c.Cooldown.MaxSec = 300
	}
	for i := range c.Providers {
		p := &c.Providers[i]
		if p.Tier == 0 {
			p.Tier = 1
		}
		p.APIKey = ResolveSecret(p.APIKey)
		if len(p.Accounts) == 0 {
			p.Accounts = []Account{{ID: DefaultAccount, APIKey: p.APIKey, ServiceAccount: p.ServiceAccount}}
		}
		for j := range p.Accounts {
			a := &p.Accounts[j]
			if a.ID == "" {
				a.ID = fmt.Sprintf("acct-%d", j+1)
			}
			a.APIKey = ResolveSecret(a.APIKey)
		}
		if p.AccountStrategy == "" {
			p.AccountStrategy = "fallback"
		}
		for j := range p.Quotas {
			p.Quotas[j].Duration, _ = time.ParseDuration(p.Quotas[j].Window)
		}
		if p.OAuth != nil {
			p.OAuth.ClientSecret = ResolveSecret(p.OAuth.ClientSecret)
		}
		p.BaseURL = strings.TrimRight(p.BaseURL, "/")
		if p.BaseURL == "" && p.Type == "vertex" && p.Project != "" {
			loc := p.Location
			if loc == "" {
				loc = "us-central1"
			}
			host := loc + "-aiplatform.googleapis.com"
			if loc == "global" {
				host = "aiplatform.googleapis.com"
			}
			p.BaseURL = "https://" + host + "/v1/projects/" + p.Project + "/locations/" + loc + "/publishers/google"
		}
		if p.BaseURL == "" {
			switch p.Type {
			case "anthropic":
				p.BaseURL = "https://api.anthropic.com"
			case "gemini":
				p.BaseURL = "https://generativelanguage.googleapis.com/v1beta"
			}
		}
	}
	if c.Saver.MaxToolChars <= 0 {
		c.Saver.MaxToolChars = 8000
	}
	for i := range c.Combos {
		if c.Combos[i].Strategy == "" {
			c.Combos[i].Strategy = "fallback"
		}
	}
}

// ResolveSecret expands "env:NAME" into the environment variable value.
func ResolveSecret(s string) string {
	if name, ok := strings.CutPrefix(s, "env:"); ok {
		return os.Getenv(name)
	}
	return s
}

func (c *Config) Validate() error {
	seen := map[string]bool{}
	for _, p := range c.Providers {
		if p.ID == "" || strings.Contains(p.ID, "/") {
			return fmt.Errorf("provider id %q must be non-empty and contain no '/'", p.ID)
		}
		if seen[p.ID] {
			return fmt.Errorf("duplicate provider id %q", p.ID)
		}
		seen[p.ID] = true
		switch p.Type {
		case "openai", "anthropic", "gemini", "vertex":
		default:
			return fmt.Errorf("provider %q: type must be openai, anthropic, gemini or vertex", p.ID)
		}
		if p.AccountStrategy != "fallback" && p.AccountStrategy != "round_robin" {
			return fmt.Errorf("provider %q: unknown account_strategy %q", p.ID, p.AccountStrategy)
		}
		accts := map[string]bool{}
		for _, a := range p.Accounts {
			if a.ServiceAccount != "" {
				if p.Type != "vertex" {
					return fmt.Errorf("provider %q: service_account is only supported for vertex", p.ID)
				}
				if _, err := gcpauth.Load(a.ServiceAccount); err != nil {
					return fmt.Errorf("provider %q account %q: %w", p.ID, a.ID, err)
				}
			}
			if accts[a.ID] || strings.ContainsAny(a.ID, "/#") {
				return fmt.Errorf("provider %q: account id %q duplicate or contains '/' or '#'", p.ID, a.ID)
			}
			accts[a.ID] = true
		}
		for _, q := range p.Quotas {
			if q.Duration <= 0 || (q.MaxRequests <= 0 && q.MaxTokens <= 0) {
				return fmt.Errorf("provider %q: quota needs a valid window and max_requests or max_tokens", p.ID)
			}
		}
		if p.BaseURL == "" {
			return fmt.Errorf("provider %q: base_url required", p.ID)
		}
		if o := p.OAuth; o != nil && (o.AuthURL == "" || o.TokenURL == "" || o.ClientID == "") {
			return fmt.Errorf("provider %q: oauth requires auth_url, token_url and client_id", p.ID)
		}
	}
	names := map[string]bool{}
	for _, cb := range c.Combos {
		if cb.Name == "" || strings.Contains(cb.Name, "/") {
			return fmt.Errorf("combo name %q must be non-empty and contain no '/'", cb.Name)
		}
		if names[cb.Name] || seen[cb.Name] {
			return fmt.Errorf("combo name %q collides with another combo or provider", cb.Name)
		}
		names[cb.Name] = true
		switch cb.Strategy {
		case "", "fallback", "round_robin", "tier":
		default:
			return fmt.Errorf("combo %q: unknown strategy %q", cb.Name, cb.Strategy)
		}
		if len(cb.Models) == 0 {
			return fmt.Errorf("combo %q has no models", cb.Name)
		}
	}
	return nil
}
