// Package provider builds upstream HTTP requests for each supported provider type.
package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"strings"

	"gorouter/internal/config"
	"gorouter/internal/translate"
)

const (
	KindOpenAI    = "openai"
	KindAnthropic = "anthropic"
	KindGemini    = "gemini"
	KindVertex    = "vertex"
)

// IsGoogle reports whether the provider speaks the Gemini generateContent API.
func (p *Provider) IsGoogle() bool { return p.Type == KindGemini || p.Type == KindVertex }

type Provider struct {
	config.Provider
}

func New(c config.Provider) *Provider { return &Provider{c} }

// BuildRequest turns an OpenAI-format body into an upstream request for model.
// A non-empty secret (e.g. an OAuth access token) overrides the configured api_key.
func (p *Provider) BuildRequest(ctx context.Context, body map[string]any, model, secret string) (*http.Request, error) {
	var (
		payload map[string]any
		url     string
	)
	switch p.Type {
	case KindAnthropic:
		payload = translate.OpenAIToAnthropic(body, model)
		url = p.BaseURL + "/v1/messages"
	case KindGemini, KindVertex:
		payload = translate.OpenAIToGemini(body, model)
		url = p.BaseURL + "/models/" + model + ":generateContent"
		if s, _ := body["stream"].(bool); s {
			url = p.BaseURL + "/models/" + model + ":streamGenerateContent?alt=sse"
		}
	default:
		payload = make(map[string]any, len(body)+1)
		for k, v := range body {
			payload[k] = v
		}
		payload["model"] = model
		if strings.Contains(p.BaseURL, "api.openai.com") {
			// OpenAI rejects unknown fields; gateways such as OpenRouter accept the markers.
			payload["messages"] = stripCacheControl(payload["messages"])
			delete(payload, "cache_control")
		}
		if s, _ := payload["stream"].(bool); s {
			payload["stream_options"] = map[string]any{"include_usage": true}
		}
		url = p.BaseURL + "/chat/completions"
	}
	b, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(b))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	oauth := secret != ""
	if secret == "" {
		secret = p.APIKey
	}
	switch {
	case p.Type == KindAnthropic:
		req.Header.Set("anthropic-version", "2023-06-01")
		if oauth {
			req.Header.Set("Authorization", "Bearer "+secret)
		} else if secret != "" {
			req.Header.Set("x-api-key", secret)
		}
	case p.Type == KindGemini:
		if secret != "" {
			if oauth {
				req.Header.Set("Authorization", "Bearer "+secret)
			} else {
				req.Header.Set("x-goog-api-key", secret)
			}
		}
	case secret != "":
		req.Header.Set("Authorization", "Bearer "+secret)
	}
	for k, v := range p.Headers {
		req.Header.Set(k, v)
	}
	return req, nil
}

// stripCacheControl returns a copy of v with every "cache_control" key removed from messages and
// their content parts; the caller's body is left untouched.
func stripCacheControl(v any) any {
	switch t := v.(type) {
	case []any:
		out := make([]any, len(t))
		for i, e := range t {
			out[i] = stripCacheControl(e)
		}
		return out
	case map[string]any:
		out := make(map[string]any, len(t))
		for k, e := range t {
			if k == "cache_control" {
				continue
			}
			if k == "content" {
				e = stripCacheControl(e)
			}
			out[k] = e
		}
		return out
	}
	return v
}
