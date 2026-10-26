package server

import (
	"fmt"
	"html"
	"io"
	"net/http"
	"strings"
	"time"

	"gorouter/internal/config"
	"gorouter/internal/dashboard"
)

func (s *Server) adminRoutes(mux *http.ServeMux) {
	s.rawRoutes(mux)
	mux.Handle("GET /dashboard/", http.StripPrefix("/dashboard", dashboard.Handler()))
	mux.HandleFunc("GET /dashboard", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/dashboard/", http.StatusFound)
	})
	mux.HandleFunc("GET /admin/usage", s.auth(func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, 200, s.usage.Snapshot())
	}))
	mux.HandleFunc("GET /admin/status", s.auth(s.status))
	mux.HandleFunc("GET /admin/config", s.auth(s.getConfig))
	mux.HandleFunc("PUT /admin/config", s.auth(s.putConfig))
	mux.HandleFunc("POST /admin/oauth/{provider}/start", s.auth(s.oauthStart))
	mux.HandleFunc("POST /admin/oauth/{provider}/device", s.auth(s.deviceStart))
	mux.HandleFunc("GET /admin/oauth/{provider}/device", s.auth(s.deviceStatus))
	mux.HandleFunc("DELETE /admin/oauth/{provider}", s.auth(s.oauthDisconnect))
	// The browser lands here after the provider redirects; protected by the one-time state value.
	mux.HandleFunc("GET /oauth/callback", s.oauthCallback)
}

func (s *Server) status(w http.ResponseWriter, _ *http.Request) {
	var ps []map[string]any
	for _, p := range s.rt().Providers() {
		e := map[string]any{"id": p.ID, "type": p.Type, "tier": p.Tier, "models": p.Models}
		var accts []map[string]any
		for _, a := range p.Accounts {
			ae := map[string]any{"id": a.ID, "disabled": a.Disabled}
			if p.OAuth != nil && s.oauth != nil {
				ok, exp := s.oauth.Status(p.ID, a.ID)
				ae["connected"] = ok
				if !exp.IsZero() {
					ae["expires_at"] = exp
				}
			}
			accts = append(accts, ae)
		}
		e["accounts"] = accts
		e["oauth"] = p.OAuth != nil
		ps = append(ps, e)
	}
	writeJSON(w, 200, map[string]any{
		"providers":   ps,
		"cooldowns":   s.rt().Health.Snapshot(),
		"quotas":      s.rt().Quota.Snapshot(s.rt()),
		"combos":      s.cfg().Combos,
		"aliases":     s.cfg().Aliases,
		"token_saver": map[string]any{"enabled": s.cfg().Saver.Enabled, "saved_chars": s.saverSaved.Load(), "saved_tool_chars": s.saverTool.Load(), "saved_history_chars": s.saverHistory.Load(), "saved_dedupe_chars": s.saverDedupe.Load()},
		"now":         time.Now(),
	})
}

// getConfig returns the active config document (secrets as configured; admin-only).
func (s *Server) getConfig(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, 200, s.cfg().Raw())
}

func (s *Server) putConfig(w http.ResponseWriter, r *http.Request) {
	raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 4<<20))
	if err != nil {
		writeError(w, 400, err.Error(), fmtOpenAI)
		return
	}
	cfg, err := config.Parse(raw)
	if err != nil {
		writeError(w, 400, err.Error(), fmtOpenAI)
		return
	}
	cfg.SetRaw(raw)
	if s.store != nil {
		if err := s.store.SaveRawConfig(raw); err != nil {
			writeError(w, 500, "save config: "+err.Error(), fmtOpenAI)
			return
		}
	}
	s.Reload(cfg)
	writeJSON(w, 200, map[string]any{"ok": true, "providers": len(cfg.Providers), "combos": len(cfg.Combos)})
}

func (s *Server) findProvider(id string) (config.Provider, bool) {
	for _, p := range s.cfg().Providers {
		if p.ID == id {
			return p, true
		}
	}
	return config.Provider{}, false
}

func (s *Server) redirectURI(r *http.Request) string {
	base := strings.TrimRight(s.cfg().PublicURL, "/")
	if base == "" {
		scheme := "http"
		if r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https" {
			scheme = "https"
		}
		base = scheme + "://" + r.Host
	}
	return base + "/oauth/callback"
}

func (s *Server) oauthStart(w http.ResponseWriter, r *http.Request) {
	p, ok := s.findProvider(r.PathValue("provider"))
	if !ok || p.OAuth == nil || s.oauth == nil {
		writeError(w, 404, "provider not found or has no oauth config", fmtOpenAI)
		return
	}
	u, err := s.oauth.Start(p, r.URL.Query().Get("account"), s.redirectURI(r))
	if err != nil {
		writeError(w, 500, err.Error(), fmtOpenAI)
		return
	}
	writeJSON(w, 200, map[string]string{"url": u, "redirect_uri": s.redirectURI(r)})
}

func (s *Server) oauthDisconnect(w http.ResponseWriter, r *http.Request) {
	if s.oauth == nil {
		writeError(w, 404, "oauth disabled", fmtOpenAI)
		return
	}
	if err := s.oauth.Disconnect(r.PathValue("provider"), r.URL.Query().Get("account")); err != nil {
		writeError(w, 500, err.Error(), fmtOpenAI)
		return
	}
	writeJSON(w, 200, map[string]bool{"ok": true})
}

func (s *Server) oauthCallback(w http.ResponseWriter, r *http.Request) {
	page := func(status int, msg string) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(status)
		fmt.Fprintf(w, `<!doctype html><meta charset=utf-8><title>gorouter</title><body style="font:16px system-ui;padding:3rem"><p>%s</p><p><a href="/dashboard/">Back to dashboard</a></p>`, html.EscapeString(msg))
	}
	if s.oauth == nil {
		page(404, "OAuth is not enabled")
		return
	}
	q := r.URL.Query()
	if e := q.Get("error"); e != "" {
		page(400, "Authorization failed: "+e+" "+q.Get("error_description"))
		return
	}
	id, err := s.oauth.Callback(r.Context(), q.Get("state"), q.Get("code"))
	if err != nil {
		page(400, "Could not complete login: "+err.Error())
		return
	}
	page(200, "Connected provider "+id+". You can close this tab.")
}

func (s *Server) deviceStart(w http.ResponseWriter, r *http.Request) {
	p, ok := s.findProvider(r.PathValue("provider"))
	if !ok || p.OAuth == nil || s.oauth == nil {
		writeError(w, 404, "provider not found or has no oauth config", fmtOpenAI)
		return
	}
	flow, err := s.oauth.StartDevice(r.Context(), p, r.URL.Query().Get("account"))
	if err != nil {
		writeError(w, 400, err.Error(), fmtOpenAI)
		return
	}
	writeJSON(w, 200, flow)
}

func (s *Server) deviceStatus(w http.ResponseWriter, r *http.Request) {
	if s.oauth == nil {
		writeError(w, 404, "oauth disabled", fmtOpenAI)
		return
	}
	writeJSON(w, 200, s.oauth.DeviceStatus(r.PathValue("provider"), r.URL.Query().Get("account")))
}
