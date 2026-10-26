// Package server exposes the OpenAI- and Anthropic-compatible HTTP API.
package server

import (
	"crypto/subtle"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"gorouter/internal/config"
	"gorouter/internal/oauth"
	"gorouter/internal/provider"
	"gorouter/internal/router"
	"gorouter/internal/tokensaver"
	"gorouter/internal/translate"
	"gorouter/internal/usage"
)

const (
	fmtOpenAI    = "openai"
	fmtAnthropic = "anthropic"
	maxBody      = 32 << 20
)

// ConfigStore persists the raw config document so edits survive restarts.
type ConfigStore interface {
	SaveRawConfig([]byte) error
}

type Options struct {
	Config *config.Config
	Store  ConfigStore           // optional; nil disables persistence of admin edits
	OAuth  *oauth.Manager        // optional; nil disables OAuth endpoints
	Quotas router.QuotaPersister // optional; nil keeps quota windows in memory only
	Usage  *usage.Tracker
}

type state struct {
	cfg *config.Config
	rt  *router.Router
}

type Server struct {
	st    atomic.Pointer[state]
	store ConfigStore
	oauth *oauth.Manager
	usage *usage.Tracker

	saverSaved   atomic.Int64 // characters removed by the token saver since start
	saverTool    atomic.Int64
	saverHistory atomic.Int64
	saverDedupe  atomic.Int64
}

func New(o Options) *Server {
	s := &Server{store: o.Store, oauth: o.OAuth, usage: o.Usage}
	rt := s.buildRouter(o.Config, nil)
	if o.Quotas != nil {
		if err := rt.Quota.Attach(o.Quotas); err != nil {
			log.Printf("quota state not restored: %v", err)
		}
	}
	s.st.Store(&state{o.Config, rt})
	return s
}

// Run flushes quota state periodically until stop is closed, then once more.
func (s *Server) Run(stop <-chan struct{}) {
	tick := time.NewTicker(10 * time.Second)
	defer tick.Stop()
	for {
		select {
		case <-tick.C:
			if err := s.rt().Quota.Flush(); err != nil {
				log.Printf("quota flush: %v", err)
			}
		case <-stop:
			_ = s.rt().Quota.Flush()
			return
		}
	}
}

func (s *Server) buildRouter(cfg *config.Config, prev *router.Router) *router.Router {
	var ts router.TokenSource
	if s.oauth != nil {
		ts = s.oauth
	}
	return router.New(cfg, ts, prev)
}

func (s *Server) cfg() *config.Config { return s.st.Load().cfg }
func (s *Server) rt() *router.Router  { return s.st.Load().rt }

// Reload atomically swaps in a new config; in-flight requests finish on the old router.
func (s *Server) Reload(cfg *config.Config) {
	old := s.st.Load()
	s.st.Store(&state{cfg, s.buildRouter(cfg, old.rt)})
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, 200, map[string]string{"status": "ok"})
	})
	mux.HandleFunc("GET /v1/models", s.auth(s.models))
	mux.HandleFunc("POST /v1/chat/completions", s.auth(func(w http.ResponseWriter, r *http.Request) { s.chat(w, r, fmtOpenAI) }))
	mux.HandleFunc("POST /v1/messages", s.auth(func(w http.ResponseWriter, r *http.Request) { s.chat(w, r, fmtAnthropic) }))
	s.adminRoutes(mux)
	return logging(mux)
}

func logging(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		next.ServeHTTP(w, r)
		log.Printf("%s %s %s", r.Method, r.URL.Path, time.Since(start).Round(time.Millisecond))
	})
}

func (s *Server) auth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if len(s.cfg().APIKeys) > 0 {
			key := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
			if key == "" {
				key = r.Header.Get("x-api-key")
			}
			ok := false
			for _, k := range s.cfg().APIKeys {
				if subtle.ConstantTimeCompare([]byte(k), []byte(key)) == 1 {
					ok = true
				}
			}
			if !ok {
				writeError(w, 401, "invalid api key", clientFormat(r))
				return
			}
		}
		next(w, r)
	}
}

func clientFormat(r *http.Request) string {
	if r.URL.Path == "/v1/messages" {
		return fmtAnthropic
	}
	return fmtOpenAI
}

func (s *Server) models(w http.ResponseWriter, _ *http.Request) {
	var data []any
	for _, m := range s.rt().Models() {
		data = append(data, map[string]any{"id": m, "object": "model", "owned_by": "gorouter"})
	}
	writeJSON(w, 200, map[string]any{"object": "list", "data": data})
}

func (s *Server) chat(w http.ResponseWriter, r *http.Request, format string) {
	raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxBody))
	if err != nil {
		writeError(w, 400, "cannot read body: "+err.Error(), format)
		return
	}
	var body map[string]any
	if err := json.Unmarshal(raw, &body); err != nil {
		writeError(w, 400, "invalid JSON: "+err.Error(), format)
		return
	}
	if format == fmtAnthropic {
		body = translate.AnthropicToOpenAI(body)
	}
	model, _ := body["model"].(string)
	if model == "" {
		writeError(w, 400, "model is required", format)
		return
	}
	cfg := s.cfg()
	if cfg.Saver.Enabled {
		saver := cfg.Saver
		if b, ok := saver.RouteBudgets[model]; ok {
			if b.MaxToolChars > 0 {
				saver.MaxToolChars = b.MaxToolChars
			}
			if b.MaxHistoryMessages > 0 {
				saver.MaxHistoryMessages = b.MaxHistoryMessages
			}
			if b.MaxPromptChars > 0 {
				saver.MaxPromptChars = b.MaxPromptChars
			}
		}
		x := tokensaver.ApplyDetailed(body, saver)
		s.saverSaved.Add(int64(x.Total))
		s.saverTool.Add(int64(x.Tool))
		s.saverHistory.Add(int64(x.History))
		s.saverDedupe.Add(int64(x.Dedupe))
	}
	stream, _ := body["stream"].(bool)

	start := time.Now()
	res, fail := s.rt().Execute(r.Context(), body, model)
	if fail != nil {
		if fail.Status != 499 {
			log.Printf("route %q failed: %s attempts=%+v", model, fail.Message, fail.Attempts)
		}
		s.recordFailures(fail, time.Since(start))
		writeError(w, fail.Status, fail.Message, format)
		return
	}
	defer res.Resp.Body.Close()
	w.Header().Set("X-Gorouter-Attempts", strconv.Itoa(len(res.Attempts)))
	p := res.Target.Provider

	rec := usage.Record{Key: res.Target.String(), PriceIn: p.PriceIn, PriceOut: p.PriceOut}
	if stream {
		rec.In, rec.Out, rec.Err = s.stream(w, res, format)
	} else {
		rec.In, rec.Out, rec.Err = s.unary(w, res, format)
	}
	rec.Latency = time.Since(start)
	s.usage.Add(rec)
	s.rt().Quota.Add(res.Target.AccountKey(), p.Quotas, 0, rec.In+rec.Out)
}

func (s *Server) unary(w http.ResponseWriter, res *router.Result, format string) (in, out int, failed bool) {
	b, err := io.ReadAll(res.Resp.Body)
	var m map[string]any
	if err == nil {
		err = json.Unmarshal(b, &m)
	}
	if err != nil {
		writeError(w, 502, "bad upstream response: "+err.Error(), format)
		return 0, 0, true
	}
	switch {
	case res.Target.Provider.Type == provider.KindAnthropic:
		m = translate.AnthropicRespToOpenAI(m)
	case res.Target.Provider.IsGoogle():
		m = translate.GeminiRespToOpenAI(m, res.Target.Model)
	}
	in, out = translate.Usage(m)
	if format == fmtAnthropic {
		m = translate.OpenAIRespToAnthropic(m)
	}
	w.Header().Set("X-Gorouter-Target", res.Target.Label())
	writeJSON(w, 200, m)
	return in, out, false
}

func (s *Server) stream(w http.ResponseWriter, res *router.Result, format string) (in, out int, failed bool) {
	fl, _ := w.(http.Flusher)
	flush := func() {
		if fl != nil {
			fl.Flush()
		}
	}
	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-cache")
	h.Set("Connection", "keep-alive")
	h.Set("X-Gorouter-Target", res.Target.Label())
	w.WriteHeader(200)
	flush()

	var emit func(map[string]any) error
	var finish func() error
	if format == fmtAnthropic {
		aw := translate.NewAnthropicWriter(w, flush)
		emit, finish = aw.Write, aw.Close
	} else {
		emit = func(c map[string]any) error {
			b, _ := json.Marshal(c)
			_, err := w.Write(append(append([]byte("data: "), b...), '\n', '\n'))
			flush()
			return err
		}
		finish = func() error {
			_, err := io.WriteString(w, "data: [DONE]\n\n")
			flush()
			return err
		}
	}
	wrapped := func(c map[string]any) error {
		if i, o := translate.Usage(c); i+o > 0 {
			in, out = i, o
		}
		return emit(c)
	}

	var err error
	switch {
	case res.Target.Provider.Type == provider.KindAnthropic:
		err = translate.AnthropicStreamToOpenAI(res.Resp.Body, wrapped)
	case res.Target.Provider.IsGoogle():
		err = translate.GeminiStreamToOpenAI(res.Resp.Body, res.Target.Model, wrapped)
	default:
		err = translate.OpenAIStreamToChunks(res.Resp.Body, wrapped)
	}
	if err != nil {
		log.Printf("stream from %s aborted: %v", res.Target, err)
		failed = true
	}
	_ = finish()
	return in, out, failed
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, msg, format string) {
	if format == fmtAnthropic {
		writeJSON(w, status, map[string]any{"type": "error", "error": map[string]any{"type": "api_error", "message": msg}})
		return
	}
	writeJSON(w, status, map[string]any{"error": map[string]any{"message": msg, "type": "gorouter_error", "code": status}})
}

// recordFailures counts every failed upstream attempt as an error against its provider/model,
// so error rates reflect routing failures that never produced a response.
func (s *Server) recordFailures(f *router.Failure, d time.Duration) {
	for _, a := range f.Attempts {
		if a.Status/100 == 2 {
			continue
		}
		key, _, _ := strings.Cut(a.Target, "@")
		s.usage.Add(usage.Record{Key: key, Err: true, Latency: d})
	}
}
