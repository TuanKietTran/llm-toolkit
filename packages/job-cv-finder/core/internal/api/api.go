// Package api exposes the core over HTTP:
//
//	/api/v1/...       candidate-facing API used by the Ruxt feature (bearer JCF_API_TOKEN,
//	                  sent only by Ruxt's server-side proxy, never by the browser)
//	/internal/v1/...  worker API for Python intelligence workers (bearer JCF_WORKER_TOKEN)
//	/healthz /readyz /metrics
//
// Responses never contain secrets: accounts expose has_secret only, task listings drop
// payloads, and errors are mapped to stable codes.
package api

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/TuanKietTran/llm-toolkit/packages/job-cv-finder/core/internal/domain"
	"github.com/TuanKietTran/llm-toolkit/packages/job-cv-finder/core/internal/secrets"
	"github.com/TuanKietTran/llm-toolkit/packages/job-cv-finder/core/internal/sources"
	"github.com/TuanKietTran/llm-toolkit/packages/job-cv-finder/core/internal/store"
	"github.com/TuanKietTran/llm-toolkit/packages/job-cv-finder/core/internal/workflow"
)

// Server wires handlers.
type Server struct {
	Engine      *workflow.Engine
	Vault       *secrets.Vault
	APIToken    string
	WorkerToken string
	Log         *slog.Logger
	Version     string

	requests atomic.Int64
	failures atomic.Int64
}

// Handler builds the HTTP handler.
func (s *Server) Handler() http.Handler {
	m := http.NewServeMux()
	pub := func(pattern string, h func(http.ResponseWriter, *http.Request) error) {
		m.Handle(pattern, s.auth(s.APIToken, h))
	}
	wrk := func(pattern string, h func(http.ResponseWriter, *http.Request) error) {
		m.Handle(pattern, s.auth(s.WorkerToken, h))
	}
	m.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) { w.Write([]byte("ok")) })
	m.HandleFunc("GET /readyz", s.ready)
	m.HandleFunc("GET /metrics", s.metrics)

	pub("GET /api/v1/integration/health", s.integrationHealth)
	pub("GET /api/v1/overview", s.overview)
	pub("GET /api/v1/sources", s.listSources)
	pub("PATCH /api/v1/sources/{id}", s.patchSource)
	pub("POST /api/v1/sources/{id}/discover", s.discover)
	pub("POST /api/v1/sources/ats", s.addATSSource)
	pub("GET /api/v1/accounts", s.listAccounts)
	pub("PUT /api/v1/accounts/{source}", s.putAccount)
	pub("PUT /api/v1/accounts/{source}/credential", s.putCredential)
	pub("GET /api/v1/jobs", s.listJobs)
	pub("GET /api/v1/jobs/{id}", s.getJob)
	pub("POST /api/v1/jobs/manual", s.manualJob)
	pub("POST /api/v1/jobs/{id}/description", s.enrichJob)
	pub("POST /api/v1/candidate/snapshots", s.postSnapshot)
	pub("GET /api/v1/candidate", s.getCandidate)
	pub("GET /api/v1/candidate/facts", s.getFacts)
	pub("GET /api/v1/applications", s.listApplications)
	pub("GET /api/v1/applications/{id}", s.getApplication)
	pub("POST /api/v1/applications/{id}/actions/{action}", s.action)
	pub("POST /api/v1/applications/{id}/documents/{doc}/registration", s.docRegistration)
	pub("POST /api/v1/intake/emails", s.intakeEmail)
	pub("GET /api/v1/intake/messages", s.listMessages)
	pub("GET /api/v1/events", s.listEvents)
	pub("GET /api/v1/events/stream", s.streamEvents)
	pub("GET /api/v1/analytics", s.analytics)
	pub("GET /api/v1/tasks", s.listTasks)

	wrk("POST /internal/v1/tasks/lease", s.leaseTask)
	wrk("POST /internal/v1/tasks/{id}/complete", s.completeTask)
	wrk("POST /internal/v1/tasks/{id}/fail", s.failTask)
	return s.observe(m)
}

// ---------------------------------------------------------------- plumbing

type httpError struct {
	status int
	code   string
	msg    string
}

func (e *httpError) Error() string { return e.msg }

func bad(msg string) error      { return &httpError{http.StatusBadRequest, "bad_request", msg} }
func notFound(msg string) error { return &httpError{http.StatusNotFound, "not_found", msg} }
func conflict(msg string) error { return &httpError{http.StatusConflict, "conflict", msg} }

func (s *Server) auth(token string, h func(http.ResponseWriter, *http.Request) error) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		if token == "" || subtle.ConstantTimeCompare([]byte(got), []byte(token)) != 1 {
			writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized", "code": "unauthorized"})
			return
		}
		if err := h(w, r); err != nil {
			s.fail(w, r, err)
		}
	})
}

func (s *Server) fail(w http.ResponseWriter, r *http.Request, err error) {
	var he *httpError
	var ve *workflow.ValidationError
	switch {
	case errors.As(err, &he):
		writeJSON(w, he.status, map[string]string{"error": he.msg, "code": he.code})
	case errors.As(err, &ve):
		writeJSON(w, http.StatusUnprocessableEntity, map[string]string{"error": ve.Error(), "code": "invalid"})
	case errors.Is(err, store.ErrNotFound):
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "not found", "code": "not_found"})
	case errors.Is(err, store.ErrConflict), errors.Is(err, store.ErrLeaseLost):
		writeJSON(w, http.StatusConflict, map[string]string{"error": err.Error(), "code": "conflict"})
	case strings.Contains(err.Error(), "illegal transition"):
		writeJSON(w, http.StatusConflict, map[string]string{"error": err.Error(), "code": "illegal_transition"})
	default:
		s.failures.Add(1)
		s.Log.Error("request failed", "method", r.Method, "path", r.URL.Path, "err", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal error", "code": "internal"})
	}
}

func (s *Server) observe(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.requests.Add(1)
		start := time.Now()
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Cache-Control", "no-store")
		next.ServeHTTP(w, r)
		if r.URL.Path != "/api/v1/events/stream" {
			s.Log.Debug("http", "method", r.Method, "path", r.URL.Path, "ms", time.Since(start).Milliseconds())
		}
	})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func readJSON(r *http.Request, v any, limit int64) error {
	dec := json.NewDecoder(io.LimitReader(r.Body, limit))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return bad("invalid JSON body: " + err.Error())
	}
	return nil
}

func ok(w http.ResponseWriter, v any) error {
	writeJSON(w, http.StatusOK, v)
	return nil
}

// ---------------------------------------------------------------- health

func (s *Server) ready(w http.ResponseWriter, r *http.Request) {
	if err := s.Engine.Store.DB.PingContext(r.Context()); err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"status": "db unavailable"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ready"})
}

func (s *Server) metrics(w http.ResponseWriter, r *http.Request) {
	counts, _ := s.Engine.Store.Counts(r.Context())
	w.Header().Set("Content-Type", "text/plain; version=0.0.4")
	fmt.Fprintf(w, "jcf_http_requests_total %d\njcf_http_failures_total %d\n", s.requests.Load(), s.failures.Load())
	for k, v := range counts {
		if st, found := strings.CutPrefix(k, "applications."); found {
			fmt.Fprintf(w, "jcf_applications{state=%q} %d\n", st, v)
		} else {
			fmt.Fprintf(w, "jcf_%s %d\n", strings.ReplaceAll(k, ".", "_"), v)
		}
	}
	var queued, leased, dead int
	s.Engine.Store.DB.QueryRowContext(r.Context(), `SELECT SUM(state='queued'), SUM(state='leased'), SUM(state='dead') FROM tasks`).Scan(nullInt(&queued), nullInt(&leased), nullInt(&dead))
	fmt.Fprintf(w, "jcf_tasks{state=\"queued\"} %d\njcf_tasks{state=\"leased\"} %d\njcf_tasks{state=\"dead\"} %d\n", queued, leased, dead)
}

type nullIntScanner struct{ p *int }

func (n nullIntScanner) Scan(v any) error {
	if x, ok := v.(int64); ok {
		*n.p = int(x)
	}
	return nil
}

func nullInt(p *int) nullIntScanner { return nullIntScanner{p} }

func (s *Server) integrationHealth(w http.ResponseWriter, r *http.Request) error {
	snap, err := s.Engine.Store.LatestCandidateSnapshot(r.Context())
	cand := map[string]any{"present": err == nil}
	if err == nil {
		cand["received_at"], cand["cvs"] = snap.ReceivedAt, len(snap.CVs)
	}
	return ok(w, map[string]any{"status": "ok", "version": s.Version, "contract_versions": map[string]int{"candidate_snapshot": 1},
		"candidate": cand, "secrets_configured": s.Vault.Configured()})
}

// ---------------------------------------------------------------- overview & analytics

func (s *Server) overview(w http.ResponseWriter, r *http.Request) error {
	ctx := r.Context()
	counts, err := s.Engine.Store.Counts(ctx)
	if err != nil {
		return err
	}
	srcs, err := s.Engine.Store.ListSources(ctx)
	if err != nil {
		return err
	}
	accts, err := s.Engine.Store.ListAccounts(ctx)
	if err != nil {
		return err
	}
	var matched int
	s.Engine.Store.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM job_matches WHERE recommendation IN ('strong_match','match')`).Scan(&matched)
	a := func(st domain.AppState) int { return counts["applications."+string(st)] }
	return ok(w, map[string]any{
		"jobs": map[string]int{"discovered": counts["jobs.unique"], "total_rows": counts["jobs.total"], "new_24h": counts["jobs.new_24h"], "matched": matched},
		"applications": map[string]int{
			"selected":   a(domain.StateSelected) + a(domain.StatePreparing),
			"prepared":   a(domain.StatePrepared) + a(domain.StatePolicyCheck) + a(domain.StateHandoffPending),
			"submitted":  a(domain.StateSubmitted),
			"failed":     a(domain.StateFailedFinal) + a(domain.StateFailedRetryable),
			"exceptions": a(domain.StateBlockedException) + a(domain.StateSubmissionUnknown),
			"handoff":    a(domain.StateHandoffPending),
			"in_flight":  a(domain.StateMatching) + a(domain.StateExecuting) + a(domain.StateVerifying),
		},
		"by_state": counts, "sources": srcs, "accounts": accts,
	})
}

func (s *Server) analytics(w http.ResponseWriter, r *http.Request) error {
	ctx := r.Context()
	type row struct {
		Source    string `json:"source"`
		Jobs      int    `json:"jobs"`
		Matched   int    `json:"matched"`
		Submitted int    `json:"submitted"`
		Handoff   int    `json:"handoff_pending"`
		Blocked   int    `json:"blocked"`
	}
	rows, err := s.Engine.Store.DB.QueryContext(ctx, `SELECT j.source_id, COUNT(DISTINCT j.id),
		SUM(CASE WHEN m.recommendation IN ('strong_match','match') THEN 1 ELSE 0 END),
		SUM(CASE WHEN a.state='SUBMITTED' THEN 1 ELSE 0 END),
		SUM(CASE WHEN a.state='HANDOFF_PENDING' THEN 1 ELSE 0 END),
		SUM(CASE WHEN a.state='BLOCKED_EXCEPTION' THEN 1 ELSE 0 END)
		FROM jobs j LEFT JOIN applications a ON a.job_id=j.id LEFT JOIN job_matches m ON m.id=a.match_id
		GROUP BY j.source_id ORDER BY j.source_id`)
	if err != nil {
		return err
	}
	defer rows.Close()
	out := []row{}
	for rows.Next() {
		var x row
		if err := rows.Scan(&x.Source, &x.Jobs, nullInt(&x.Matched), nullInt(&x.Submitted), nullInt(&x.Handoff), nullInt(&x.Blocked)); err != nil {
			return err
		}
		out = append(out, x)
	}
	var avg float64
	s.Engine.Store.DB.QueryRowContext(ctx, `SELECT COALESCE(AVG(match_score),0) FROM job_matches`).Scan(&avg)
	return ok(w, map[string]any{"by_source": out, "average_match_score": avg})
}

// ---------------------------------------------------------------- sources & accounts

func (s *Server) listSources(w http.ResponseWriter, r *http.Request) error {
	srcs, err := s.Engine.Store.ListSources(r.Context())
	if err != nil {
		return err
	}
	return ok(w, map[string]any{"sources": srcs})
}

func (s *Server) patchSource(w http.ResponseWriter, r *http.Request) error {
	var body struct {
		Enabled *bool `json:"enabled"`
	}
	if err := readJSON(r, &body, 1<<10); err != nil {
		return err
	}
	if body.Enabled == nil {
		return bad("only 'enabled' can be changed here; capability policy changes are code-reviewed (sources/registry.go)")
	}
	if err := s.Engine.Store.SetSourceEnabled(r.Context(), r.PathValue("id"), *body.Enabled); err != nil {
		return err
	}
	return ok(w, map[string]bool{"enabled": *body.Enabled})
}

func (s *Server) addATSSource(w http.ResponseWriter, r *http.Request) error {
	var body struct {
		Kind    string `json:"kind"`    // greenhouse|lever
		Token   string `json:"token"`   // board token / site name
		Company string `json:"company"` // display name
	}
	if err := readJSON(r, &body, 1<<10); err != nil {
		return err
	}
	body.Token = strings.TrimSpace(body.Token)
	if body.Token == "" || strings.ContainsAny(body.Token, "/?#") || len(body.Token) > 100 {
		return bad("token must be a board token or site name")
	}
	var src store.Source
	switch body.Kind {
	case sources.KindGreenhouse:
		src = sources.GreenhouseBoard(body.Token, body.Company)
	case sources.KindLever:
		src = sources.LeverSite(body.Token, body.Company)
	default:
		return bad("kind must be greenhouse or lever")
	}
	src.Config["company"] = body.Company
	if err := s.Engine.Store.UpsertSource(r.Context(), src); err != nil {
		return err
	}
	writeJSON(w, http.StatusCreated, src)
	return nil
}

func (s *Server) discover(w http.ResponseWriter, r *http.Request) error {
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Minute)
	defer cancel()
	res, err := s.Engine.RunDiscovery(ctx, r.PathValue("id"), "")
	if err != nil {
		return err
	}
	return ok(w, res)
}

func (s *Server) listAccounts(w http.ResponseWriter, r *http.Request) error {
	accts, err := s.Engine.Store.ListAccounts(r.Context())
	if err != nil {
		return err
	}
	return ok(w, map[string]any{"accounts": accts})
}

func (s *Server) putAccount(w http.ResponseWriter, r *http.Request) error {
	var body struct {
		Label string `json:"label"`
	}
	if err := readJSON(r, &body, 1<<10); err != nil {
		return err
	}
	src := r.PathValue("source")
	if _, err := s.Engine.Store.GetSource(r.Context(), src); err != nil {
		return err
	}
	existing, err := s.Engine.Store.GetAccountBySource(r.Context(), src)
	id := store.NewID("acct")
	status := "not_connected"
	if err == nil {
		id, status = existing.ID, existing.Status
	}
	if err := s.Engine.Store.UpsertAccount(r.Context(), store.Account{ID: id, SourceID: src, Label: strings.TrimSpace(body.Label), Status: status}); err != nil {
		return err
	}
	return ok(w, map[string]string{"id": id, "status": status})
}

// putCredential stores a credential for an account. It is write-only: no endpoint returns it.
// Only sources with an authorized-API submit capability accept one; platform passwords are
// refused because no permitted executor would use them.
func (s *Server) putCredential(w http.ResponseWriter, r *http.Request) error {
	var body struct {
		Credential string `json:"credential"`
	}
	if err := readJSON(r, &body, 8<<10); err != nil {
		return err
	}
	ctx := r.Context()
	src, err := s.Engine.Store.GetSource(ctx, r.PathValue("source"))
	if err != nil {
		return err
	}
	if m, permitted := src.Policy.Permits(domain.CapSubmitApplication); !permitted || m != domain.MethodAuthorizedAPI {
		return bad("this source has no authorized-API capability; the system does not store platform passwords or browser sessions for it")
	}
	if strings.TrimSpace(body.Credential) == "" {
		return bad("credential is empty")
	}
	acct, err := s.Engine.Store.GetAccountBySource(ctx, src.ID)
	if err != nil {
		acct = store.Account{ID: store.NewID("acct"), SourceID: src.ID, Label: src.Name}
	}
	ref := "account:" + src.ID
	if err := s.Vault.Put(ctx, ref, []byte(body.Credential)); err != nil {
		if errors.Is(err, secrets.ErrNoKey) {
			return &httpError{http.StatusServiceUnavailable, "secrets_unconfigured", err.Error()}
		}
		return err
	}
	now := time.Now().UTC().Format(time.RFC3339)
	acct.SecretRef, acct.Status, acct.LastAuthAt = &ref, "connected", &now
	if err := s.Engine.Store.UpsertAccount(ctx, acct); err != nil {
		return err
	}
	_ = s.Engine.Store.RecordSession(ctx, acct.ID, "valid", nil, "")
	_ = s.Engine.Store.Event(ctx, s.Engine.Store.DB, "", "account.credential_set", "", "", map[string]string{"source_id": src.ID})
	return ok(w, map[string]any{"status": "connected", "has_secret": true})
}

// ---------------------------------------------------------------- jobs

func (s *Server) listJobs(w http.ResponseWriter, r *http.Request) error {
	q := r.URL.Query()
	limit, _ := strconv.Atoi(q.Get("limit"))
	offset, _ := strconv.Atoi(q.Get("offset"))
	jobs, err := s.Engine.Store.ListJobs(r.Context(), store.JobFilter{SourceID: q.Get("source"), OnlyPrimary: q.Get("all") != "1", Query: q.Get("q"), Limit: limit, Offset: offset})
	if err != nil {
		return err
	}
	if jobs == nil {
		jobs = []domain.Job{}
	}
	return ok(w, map[string]any{"jobs": jobs})
}

func (s *Server) getJob(w http.ResponseWriter, r *http.Request) error {
	j, err := s.Engine.Store.GetJob(r.Context(), r.PathValue("id"))
	if err != nil {
		return err
	}
	dups, _ := s.Engine.Store.FindJobsByCanonicalKey(r.Context(), j.CanonicalKey)
	return ok(w, map[string]any{"job": j, "same_job_on": dups})
}

func (s *Server) manualJob(w http.ResponseWriter, r *http.Request) error {
	var body struct {
		Source      string `json:"source"` // manual|linkedin|itviec|careerviet|vietnamworks
		URL         string `json:"url"`
		Company     string `json:"company"`
		Title       string `json:"title"`
		Location    string `json:"location"`
		Description string `json:"description"`
		Salary      string `json:"salary"`
	}
	if err := readJSON(r, &body, 256<<10); err != nil {
		return err
	}
	if body.Source == "" {
		body.Source = sources.KindManual
	}
	src, err := s.Engine.Store.GetSource(r.Context(), body.Source)
	if err != nil {
		return bad("unknown source " + body.Source)
	}
	if m, permitted := src.Policy.Permits(domain.CapReadJobDetails); !permitted || m != domain.MethodUserProvided {
		if m2, p2 := src.Policy.Permits(domain.CapDiscoverJobs); !p2 || (m2 != domain.MethodUserProvided && m2 != domain.MethodEmailIntake) {
			return bad("source does not accept user-provided jobs")
		}
	}
	if strings.TrimSpace(body.Company) == "" || strings.TrimSpace(body.Title) == "" {
		return bad("company and title are required")
	}
	ext := externalIDFromURL(body.Source, body.URL)
	if ext == "" {
		ext = "manual:" + store.NewID("j")
	}
	raw, _ := json.Marshal(body)
	res, err := s.Engine.IngestJobs(r.Context(), body.Source, []domain.RawJob{{ExternalJobID: ext, Company: body.Company, Title: body.Title,
		Location: body.Location, Description: body.Description, SalaryText: body.Salary, ApplicationURL: body.URL,
		Partial: strings.TrimSpace(body.Description) == "", RawContentType: "application/json", Raw: raw}})
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusCreated, res)
	return nil
}

func externalIDFromURL(source, u string) string {
	if source == sources.KindLinkedIn {
		if i := strings.Index(u, "/jobs/view/"); i >= 0 {
			rest := u[i+len("/jobs/view/"):]
			id := strings.FieldsFunc(rest, func(c rune) bool { return c < '0' || c > '9' })
			if len(id) > 0 && len(id[0]) >= 6 {
				return id[0] // same id as alert/confirmation emails
			}
		}
	}
	return ""
}

func (s *Server) enrichJob(w http.ResponseWriter, r *http.Request) error {
	var body struct {
		Description string `json:"description"`
	}
	if err := readJSON(r, &body, 256<<10); err != nil {
		return err
	}
	if strings.TrimSpace(body.Description) == "" {
		return bad("description is empty")
	}
	j, err := s.Engine.Store.GetJob(r.Context(), r.PathValue("id"))
	if err != nil {
		return err
	}
	res, err := s.Engine.IngestJobs(r.Context(), j.SourceID, []domain.RawJob{{ExternalJobID: j.ExternalJobID, Company: j.Company, Title: j.Title,
		Location: j.Location, Description: body.Description, ApplicationURL: j.ApplicationURL, RawContentType: "text/plain", Raw: []byte(body.Description)}})
	if err != nil {
		return err
	}
	return ok(w, res)
}

// ---------------------------------------------------------------- candidate (Ruxt integration)

func (s *Server) postSnapshot(w http.ResponseWriter, r *http.Request) error {
	raw, err := io.ReadAll(io.LimitReader(r.Body, 8<<20))
	if err != nil {
		return err
	}
	res, err := s.Engine.IngestCandidateSnapshot(r.Context(), raw)
	if err != nil {
		return err
	}
	res.Payload = nil
	status := http.StatusOK
	if res.Created {
		status = http.StatusCreated
	}
	writeJSON(w, status, res)
	return nil
}

func (s *Server) getCandidate(w http.ResponseWriter, r *http.Request) error {
	snap, err := s.Engine.Store.LatestCandidateSnapshot(r.Context())
	if errors.Is(err, store.ErrNotFound) {
		return ok(w, map[string]any{"snapshot": nil})
	}
	if err != nil {
		return err
	}
	snap.Payload = nil // Ruxt already has the data; do not echo candidate content back
	return ok(w, map[string]any{"snapshot": snap})
}

func (s *Server) getFacts(w http.ResponseWriter, r *http.Request) error {
	snap, err := s.Engine.Store.LatestCandidateSnapshot(r.Context())
	if err != nil {
		return err
	}
	facts, err := s.Engine.Store.ListFacts(r.Context(), snap.ID)
	if err != nil {
		return err
	}
	if facts == nil {
		facts = []store.Fact{}
	}
	return ok(w, map[string]any{"snapshot_id": snap.ID, "facts": facts})
}

// ---------------------------------------------------------------- applications

func (s *Server) listApplications(w http.ResponseWriter, r *http.Request) error {
	var states []domain.AppState
	for _, st := range strings.Split(r.URL.Query().Get("state"), ",") {
		if st = strings.TrimSpace(st); st != "" {
			states = append(states, domain.AppState(st))
		}
	}
	apps, err := s.Engine.Store.ListApplications(r.Context(), states, 500)
	if err != nil {
		return err
	}
	type item struct {
		store.Application
		Job   domain.Job      `json:"job"`
		Match json.RawMessage `json:"match,omitempty"`
	}
	out := []item{}
	for _, a := range apps {
		j, err := s.Engine.Store.GetJob(r.Context(), a.JobID)
		if err != nil {
			return err
		}
		j.Description = "" // list view: keep payload small
		it := item{Application: a, Job: j}
		if a.MatchID != nil {
			if m, err := s.Engine.Store.GetMatch(r.Context(), *a.MatchID); err == nil {
				it.Match = summarizeMatch(m)
			}
		}
		out = append(out, it)
	}
	return ok(w, map[string]any{"applications": out})
}

func summarizeMatch(raw json.RawMessage) json.RawMessage {
	var m map[string]any
	if json.Unmarshal(raw, &m) != nil {
		return nil
	}
	delete(m, "facts")
	b, _ := json.Marshal(m)
	return b
}

func (s *Server) getApplication(w http.ResponseWriter, r *http.Request) error {
	ctx := r.Context()
	a, err := s.Engine.Store.GetApplication(ctx, r.PathValue("id"))
	if err != nil {
		return err
	}
	j, err := s.Engine.Store.GetJob(ctx, a.JobID)
	if err != nil {
		return err
	}
	var match json.RawMessage
	if a.MatchID != nil {
		match, _ = s.Engine.Store.GetMatch(ctx, *a.MatchID)
	}
	docs, _ := s.Engine.Store.ListDocuments(ctx, a.ID)
	answers, _ := s.Engine.Store.ListAnswers(ctx, a.ID)
	runs, _ := s.Engine.Store.ListRuns(ctx, a.ID)
	receipts, _ := s.Engine.Store.ListReceipts(ctx, a.ID)
	pol, _ := s.Engine.Store.ListPolicyDecisions(ctx, a.ID)
	events, _ := s.Engine.Store.ListEvents(ctx, 0, a.ID, 1000)
	src, _ := s.Engine.Store.GetSource(ctx, j.SourceID)
	return ok(w, map[string]any{"application": a, "job": j, "source": src, "match": match, "documents": docs, "answers": answers,
		"runs": runs, "receipts": receipts, "policy_decisions": pol, "events": events})
}

func (s *Server) action(w http.ResponseWriter, r *http.Request) error {
	var body struct {
		Note       string `json:"note"`
		QuestionID string `json:"question_id"`
		Answer     string `json:"answer"`
		Evidence   string `json:"evidence"`
		URL        string `json:"url"`
		Outcome    string `json:"outcome"`
	}
	if r.ContentLength != 0 {
		if err := readJSON(r, &body, 32<<10); err != nil {
			return err
		}
	}
	ctx, id := r.Context(), r.PathValue("id")
	var err error
	switch r.PathValue("action") {
	case "select":
		err = s.Engine.OverrideSelect(ctx, id, body.Note)
	case "rematch":
		err = s.Engine.Rematch(ctx, id)
	case "retry":
		err = s.Engine.Retry(ctx, id)
	case "regenerate":
		err = s.Engine.Regenerate(ctx, id)
	case "answer":
		err = s.Engine.ProvideAnswer(ctx, id, body.QuestionID, body.Answer)
	case "attest-submitted":
		err = s.Engine.AttestSubmitted(ctx, id, body.Evidence, body.URL)
	case "outcome":
		err = s.Engine.RecordOutcome(ctx, id, body.Outcome)
	default:
		return notFound("unknown action")
	}
	if err != nil {
		return err
	}
	a, err := s.Engine.Store.GetApplication(ctx, id)
	if err != nil {
		return err
	}
	return ok(w, a)
}

// docRegistration records that Ruxt imported a generated document as a new local CV
// session (Ruxt's local-first equivalent of "register artifact").
func (s *Server) docRegistration(w http.ResponseWriter, r *http.Request) error {
	var body struct {
		Status   string `json:"status"` // registered|declined
		RuxtCVID string `json:"ruxt_cv_id"`
	}
	if err := readJSON(r, &body, 1<<10); err != nil {
		return err
	}
	if body.Status != "registered" && body.Status != "declined" {
		return bad("status must be registered or declined")
	}
	docs, err := s.Engine.Store.ListDocuments(r.Context(), r.PathValue("id"))
	if err != nil {
		return err
	}
	for _, d := range docs {
		if d.ID == r.PathValue("doc") {
			if err := s.Engine.Store.SetDocumentRegistration(r.Context(), d.ID, body.Status); err != nil {
				return err
			}
			_ = s.Engine.Store.Event(r.Context(), s.Engine.Store.DB, d.ApplicationID, "document.registration", "", "", map[string]string{"document_id": d.ID, "status": body.Status, "ruxt_cv_id": body.RuxtCVID})
			return ok(w, map[string]string{"status": body.Status})
		}
	}
	return notFound("document not found")
}

// ---------------------------------------------------------------- intake

func (s *Server) intakeEmail(w http.ResponseWriter, r *http.Request) error {
	var e sources.Email
	if strings.HasPrefix(r.Header.Get("Content-Type"), "message/rfc822") {
		raw, err := io.ReadAll(io.LimitReader(r.Body, 20<<20))
		if err != nil {
			return err
		}
		if e, err = sources.ParseRFC822(raw); err != nil {
			return bad(err.Error())
		}
	} else if err := readJSON(r, &e, 4<<20); err != nil {
		return err
	}
	if e.Date.IsZero() {
		e.Date = time.Now().UTC()
	}
	res, err := s.Engine.IntakeEmail(r.Context(), e)
	if err != nil {
		return err
	}
	return ok(w, res)
}

func (s *Server) listMessages(w http.ResponseWriter, r *http.Request) error {
	rows, err := s.Engine.Store.DB.QueryContext(r.Context(), `SELECT message_id, sender, subject, received_at, classification, parsed, processed_at FROM inbound_messages ORDER BY processed_at DESC LIMIT 200`)
	if err != nil {
		return err
	}
	defer rows.Close()
	out := []map[string]any{}
	for rows.Next() {
		var id, from, subj, rec, cls, parsed, proc string
		if err := rows.Scan(&id, &from, &subj, &rec, &cls, &parsed, &proc); err != nil {
			return err
		}
		out = append(out, map[string]any{"message_id": id, "from": from, "subject": subj, "received_at": rec, "classification": cls, "parsed": json.RawMessage(parsed), "processed_at": proc})
	}
	return ok(w, map[string]any{"messages": out})
}

// ---------------------------------------------------------------- events

func (s *Server) listEvents(w http.ResponseWriter, r *http.Request) error {
	after, _ := strconv.ParseInt(r.URL.Query().Get("after"), 10, 64)
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	evs, err := s.Engine.Store.ListEvents(r.Context(), after, r.URL.Query().Get("application_id"), limit)
	if err != nil {
		return err
	}
	if evs == nil {
		evs = []store.EventRow{}
	}
	return ok(w, map[string]any{"events": evs})
}

// streamEvents is SSE over the durable event log: clients resume with Last-Event-ID, so no
// progress update is lost across reconnects or server restarts.
func (s *Server) streamEvents(w http.ResponseWriter, r *http.Request) error {
	fl, okf := w.(http.Flusher)
	if !okf {
		return fmt.Errorf("streaming unsupported")
	}
	after, _ := strconv.ParseInt(r.Header.Get("Last-Event-ID"), 10, 64)
	if q := r.URL.Query().Get("after"); q != "" {
		after, _ = strconv.ParseInt(q, 10, 64)
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	fl.Flush()
	tick := time.NewTicker(time.Second)
	defer tick.Stop()
	keep := time.NewTicker(20 * time.Second)
	defer keep.Stop()
	for {
		evs, err := s.Engine.Store.ListEvents(r.Context(), after, r.URL.Query().Get("application_id"), 200)
		if err != nil {
			return nil
		}
		for _, ev := range evs {
			b, _ := json.Marshal(ev)
			fmt.Fprintf(w, "id: %d\nevent: %s\ndata: %s\n\n", ev.Seq, ev.Kind, b)
			after = ev.Seq
		}
		if len(evs) > 0 {
			fl.Flush()
		}
		select {
		case <-r.Context().Done():
			return nil
		case <-keep.C:
			fmt.Fprint(w, ": keepalive\n\n")
			fl.Flush()
		case <-tick.C:
		}
	}
}

func (s *Server) listTasks(w http.ResponseWriter, r *http.Request) error {
	ts, err := s.Engine.Store.ListTasks(r.Context(), 200)
	if err != nil {
		return err
	}
	if ts == nil {
		ts = []store.Task{}
	}
	return ok(w, map[string]any{"tasks": ts})
}

// ---------------------------------------------------------------- worker API

func (s *Server) leaseTask(w http.ResponseWriter, r *http.Request) error {
	var body struct {
		Kinds        []string `json:"kinds"`
		Worker       string   `json:"worker"`
		LeaseSeconds int      `json:"lease_seconds"`
	}
	if err := readJSON(r, &body, 4<<10); err != nil {
		return err
	}
	if len(body.Kinds) == 0 || body.Worker == "" {
		return bad("kinds and worker are required")
	}
	if body.LeaseSeconds <= 0 || body.LeaseSeconds > 3600 {
		body.LeaseSeconds = 300
	}
	t, err := s.Engine.Store.Lease(r.Context(), body.Kinds, body.Worker, time.Duration(body.LeaseSeconds)*time.Second)
	if err != nil {
		return err
	}
	if t == nil {
		w.WriteHeader(http.StatusNoContent)
		return nil
	}
	return ok(w, t)
}

func (s *Server) completeTask(w http.ResponseWriter, r *http.Request) error {
	var body struct {
		Worker string          `json:"worker"`
		Result json.RawMessage `json:"result"`
	}
	if err := readJSON(r, &body, 8<<20); err != nil {
		return err
	}
	if err := s.Engine.CompleteTask(r.Context(), r.PathValue("id"), body.Worker, body.Result); err != nil {
		return err
	}
	return ok(w, map[string]string{"status": "done"})
}

func (s *Server) failTask(w http.ResponseWriter, r *http.Request) error {
	var body struct {
		Worker    string `json:"worker"`
		Error     string `json:"error"`
		Retryable bool   `json:"retryable"`
	}
	if err := readJSON(r, &body, 64<<10); err != nil {
		return err
	}
	state, err := s.Engine.FailTask(r.Context(), r.PathValue("id"), body.Worker, body.Error, body.Retryable)
	if err != nil {
		return err
	}
	return ok(w, map[string]string{"status": state})
}

var _ = conflict
