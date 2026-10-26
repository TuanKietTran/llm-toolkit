package server

import (
	"bytes"
	"encoding/json"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"strconv"
	"strings"
	"time"

	"gorouter/internal/provider"
	"gorouter/internal/router"
	"gorouter/internal/usage"
)

// rawPaths are OpenAI endpoints forwarded as-is (model rewritten) to openai-type providers,
// with the same combo/fallback/cooldown/quota behaviour as chat.
var rawPaths = []string{
	"/v1/embeddings",
	"/v1/images/generations",
	"/v1/audio/speech",
	"/v1/audio/transcriptions",
	"/v1/audio/translations",
}

func (s *Server) rawRoutes(mux *http.ServeMux) {
	for _, p := range rawPaths {
		path := p
		mux.HandleFunc("POST "+path, s.auth(func(w http.ResponseWriter, r *http.Request) { s.raw(w, r, path) }))
	}
}

// bodyBuilder produces the request body for a given upstream model.
type bodyBuilder func(model string) (io.Reader, string, error)

func (s *Server) readRaw(w http.ResponseWriter, r *http.Request) (model string, build bodyBuilder, err error) {
	r.Body = http.MaxBytesReader(w, r.Body, maxBody)
	ct, params, _ := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if strings.HasPrefix(ct, "multipart/") {
		if err := r.ParseMultipartForm(maxBody); err != nil {
			return "", nil, err
		}
		_ = params
		form := r.MultipartForm
		model = r.FormValue("model")
		return model, func(upModel string) (io.Reader, string, error) {
			var buf bytes.Buffer
			mw := multipart.NewWriter(&buf)
			for k, vs := range form.Value {
				for _, v := range vs {
					if k == "model" {
						v = upModel
					}
					if err := mw.WriteField(k, v); err != nil {
						return nil, "", err
					}
				}
			}
			for k, fs := range form.File {
				for _, fh := range fs {
					f, err := fh.Open()
					if err != nil {
						return nil, "", err
					}
					part, err := mw.CreateFormFile(k, fh.Filename)
					if err == nil {
						_, err = io.Copy(part, f)
					}
					f.Close()
					if err != nil {
						return nil, "", err
					}
				}
			}
			if err := mw.Close(); err != nil {
				return nil, "", err
			}
			return &buf, mw.FormDataContentType(), nil
		}, nil
	}
	raw, err := io.ReadAll(r.Body)
	if err != nil {
		return "", nil, err
	}
	var body map[string]any
	if err := json.Unmarshal(raw, &body); err != nil {
		return "", nil, err
	}
	model, _ = body["model"].(string)
	return model, func(upModel string) (io.Reader, string, error) {
		body["model"] = upModel
		b, err := json.Marshal(body)
		return bytes.NewReader(b), "application/json", err
	}, nil
}

func (s *Server) raw(w http.ResponseWriter, r *http.Request, path string) {
	model, build, err := s.readRaw(w, r)
	if err != nil {
		writeError(w, 400, "invalid request: "+err.Error(), fmtOpenAI)
		return
	}
	if model == "" {
		writeError(w, 400, "model is required", fmtOpenAI)
		return
	}
	start := time.Now()
	res, fail := s.rt().Run(r.Context(), model,
		func(p *provider.Provider) bool { return p.Type == provider.KindOpenAI },
		func(t router.Target, p *provider.Provider, secret string) (*http.Request, error) {
			body, ct, err := build(t.Model)
			if err != nil {
				return nil, err
			}
			req, err := http.NewRequestWithContext(r.Context(), http.MethodPost, p.BaseURL+strings.TrimPrefix(path, "/v1"), body)
			if err != nil {
				return nil, err
			}
			req.Header.Set("Content-Type", ct)
			if secret == "" {
				secret = p.APIKey
			}
			if secret != "" {
				req.Header.Set("Authorization", "Bearer "+secret)
			}
			for k, v := range p.Headers {
				req.Header.Set(k, v)
			}
			return req, nil
		})
	if fail != nil {
		s.recordFailures(fail, time.Since(start))
		writeError(w, fail.Status, fail.Message, fmtOpenAI)
		return
	}
	defer res.Resp.Body.Close()
	w.Header().Set("X-Gorouter-Attempts", strconv.Itoa(len(res.Attempts)))

	rec := usage.Record{Key: res.Target.String(), PriceIn: res.Target.Provider.PriceIn, PriceOut: res.Target.Provider.PriceOut}
	ct := res.Resp.Header.Get("Content-Type")
	w.Header().Set("X-Gorouter-Target", res.Target.Label())
	if ct != "" {
		w.Header().Set("Content-Type", ct)
	}
	if strings.HasPrefix(ct, "application/json") {
		b, err := io.ReadAll(io.LimitReader(res.Resp.Body, maxBody))
		if err != nil {
			rec.Err = true
		}
		var m map[string]any
		if json.Unmarshal(b, &m) == nil {
			if u, ok := m["usage"].(map[string]any); ok {
				in, _ := u["prompt_tokens"].(float64)
				rec.In = int(in)
			}
		}
		w.WriteHeader(res.Resp.StatusCode)
		_, _ = w.Write(b)
	} else {
		w.WriteHeader(res.Resp.StatusCode)
		if _, err := io.Copy(w, res.Resp.Body); err != nil {
			rec.Err = true
		}
	}
	rec.Latency = time.Since(start)
	s.usage.Add(rec)
	s.rt().Quota.Add(res.Target.AccountKey(), res.Target.Provider.Quotas, 0, rec.In)
}
