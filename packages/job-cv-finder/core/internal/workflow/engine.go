// Package workflow is the durable orchestrator. Every application advances only through
// Engine.Step, which reads persisted state, performs one idempotent step, and commits the
// transition (with its event) atomically. A crash at any point leaves a state from which
// Step can resume; the only non-idempotent action (an external submission) is bracketed by a
// write-ahead run record and reconciled through executor lookups.
package workflow

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"sync"
	"time"

	"github.com/TuanKietTran/llm-toolkit/packages/job-cv-finder/core/contracts"
	"github.com/TuanKietTran/llm-toolkit/packages/job-cv-finder/core/internal/domain"
	"github.com/TuanKietTran/llm-toolkit/packages/job-cv-finder/core/internal/execution"
	"github.com/TuanKietTran/llm-toolkit/packages/job-cv-finder/core/internal/normalize"
	"github.com/TuanKietTran/llm-toolkit/packages/job-cv-finder/core/internal/policy"
	"github.com/TuanKietTran/llm-toolkit/packages/job-cv-finder/core/internal/sources"
	"github.com/TuanKietTran/llm-toolkit/packages/job-cv-finder/core/internal/store"
)

// Task kinds served to the Python intelligence workers.
const (
	TaskMatch   = "intel.match"
	TaskPrepare = "intel.prepare"
)

// Config tunes the engine.
type Config struct {
	Policy          policy.Config
	MaxExecAttempts int           // submission attempts before FAILED_FINAL
	WaitRecheck     time.Duration // how often waiting states re-assert their task
	ReconcileFor    time.Duration // how long SUBMISSION_UNKNOWN keeps reconciling before escalating
	DiscoverEvery   time.Duration
	TickEvery       time.Duration
}

// Engine runs workflows.
type Engine struct {
	Store     *store.Store
	Registry  *sources.Registry
	Executors map[domain.AccessMethod]execution.Executor
	Cfg       Config
	Log       *slog.Logger

	wake     chan struct{}
	inflight sync.Map // app id -> struct{}: submissions running in THIS process
	limiter  sync.Map // source id -> chan struct{}: per-source execution concurrency
}

// New builds an engine with defaults filled in.
func New(st *store.Store, reg *sources.Registry, execs map[domain.AccessMethod]execution.Executor, cfg Config, log *slog.Logger) *Engine {
	if cfg.MaxExecAttempts == 0 {
		cfg.MaxExecAttempts = 3
	}
	if cfg.WaitRecheck == 0 {
		cfg.WaitRecheck = 5 * time.Minute
	}
	if cfg.ReconcileFor == 0 {
		cfg.ReconcileFor = 24 * time.Hour
	}
	if cfg.DiscoverEvery == 0 {
		cfg.DiscoverEvery = 6 * time.Hour
	}
	if cfg.TickEvery == 0 {
		cfg.TickEvery = 2 * time.Second
	}
	if log == nil {
		log = slog.Default()
	}
	return &Engine{Store: st, Registry: reg, Executors: execs, Cfg: cfg, Log: log, wake: make(chan struct{}, 1)}
}

// Wake asks the loop to run a tick soon.
func (e *Engine) Wake() {
	select {
	case e.wake <- struct{}{}:
	default:
	}
}

// ---------------------------------------------------------------- candidate snapshots

// SnapshotSummary is what the intake returns.
type SnapshotSummary struct {
	store.CandidateSnapshot
	NewApplications int `json:"new_applications"`
}

// IngestCandidateSnapshot validates and stores a Ruxt snapshot, then starts matching for
// every unique active job that has no application yet.
func (e *Engine) IngestCandidateSnapshot(ctx context.Context, raw []byte) (SnapshotSummary, error) {
	if err := contracts.Validate(contracts.CandidateSnapshot, raw); err != nil {
		return SnapshotSummary{}, &ValidationError{Err: err}
	}
	var p struct {
		Profile struct {
			ProfileID string `json:"profile_id"`
			UpdatedAt string `json:"updated_at"`
		} `json:"profile"`
		CVs []store.CVRef `json:"cvs"`
	}
	if err := json.Unmarshal(raw, &p); err != nil {
		return SnapshotSummary{}, &ValidationError{Err: err}
	}
	snap, err := e.Store.SaveCandidateSnapshot(ctx, p.Profile.ProfileID, p.Profile.UpdatedAt, p.CVs, raw)
	if err != nil {
		return SnapshotSummary{}, err
	}
	out := SnapshotSummary{CandidateSnapshot: snap}
	jobs, err := e.Store.ListJobs(ctx, store.JobFilter{OnlyPrimary: true, Limit: 500})
	if err != nil {
		return out, err
	}
	for _, j := range jobs {
		if j.Status == "expired" {
			continue
		}
		if _, created, err := e.startApplication(ctx, j, snap.ID); err != nil {
			return out, err
		} else if created {
			out.NewApplications++
		}
	}
	e.Wake()
	return out, nil
}

// ValidationError marks bad input (HTTP 400/422), as opposed to internal failures.
type ValidationError struct{ Err error }

func (v *ValidationError) Error() string { return v.Err.Error() }
func (v *ValidationError) Unwrap() error { return v.Err }

func (e *Engine) startApplication(ctx context.Context, j domain.Job, snapshotID string) (store.Application, bool, error) {
	return e.Store.CreateApplication(ctx, j, snapshotID, func(tx *sql.Tx, app *store.Application) error {
		return e.enqueueMatch(ctx, tx, *app, j)
	})
}

// ------------------------------------------------------------------- job intake

// IngestResult counts what happened to a batch of jobs.
type IngestResult struct {
	Found, New, Changed, Duplicates, Applications int
	JobIDs                                        []string
}

// IngestJobs normalizes, deduplicates and persists jobs from one source, then starts
// applications for new unique jobs when candidate data is available.
func (e *Engine) IngestJobs(ctx context.Context, sourceID string, raws []domain.RawJob) (IngestResult, error) {
	res := IngestResult{Found: len(raws)}
	snap, err := e.Store.LatestCandidateSnapshot(ctx)
	haveSnap := err == nil
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		return res, err
	}
	for _, r := range raws {
		j := normalize.Normalize(sourceID, r, e.Store.Now())
		if j.ExternalJobID == "" || j.Title == "" || j.Company == "" {
			e.Log.Warn("job skipped: missing identity fields", "source", sourceID)
			continue
		}
		stored, outcome, err := e.Store.IngestJob(ctx, j, r.RawContentType, r.Raw)
		if err != nil {
			return res, err
		}
		res.JobIDs = append(res.JobIDs, stored.ID)
		switch outcome {
		case store.IngestNew:
			res.New++
		case store.IngestChanged:
			res.Changed++
			if err := e.rematchIfWaiting(ctx, stored); err != nil {
				return res, err
			}
		case store.IngestDuplicate:
			res.Duplicates++
		}
		if outcome == store.IngestNew && haveSnap {
			if _, created, err := e.startApplication(ctx, stored, snap.ID); err != nil {
				return res, err
			} else if created {
				res.Applications++
			}
		}
	}
	e.Wake()
	return res, nil
}

// rematchIfWaiting re-runs matching when a job gains content (e.g. the candidate pasted the
// full description for an email-alert job) and its application has not progressed past selection.
func (e *Engine) rematchIfWaiting(ctx context.Context, j domain.Job) error {
	app, err := e.Store.ApplicationByLogicalKey(ctx, j.CanonicalKey)
	if errors.Is(err, store.ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	if app.State != domain.StateMatched && app.State != domain.StateNotSelected {
		return nil
	}
	return e.Store.Tx(ctx, func(tx *sql.Tx) error {
		if err := e.Store.Transition(ctx, tx, &app, domain.StateMatching, store.Patch{ClearNextAttempt: true}, map[string]any{"reason": "job content changed"}); err != nil {
			return err
		}
		return e.enqueueMatch(ctx, tx, app, j)
	})
}

// RunDiscovery polls one source (only sources whose policy permits programmatic discovery).
func (e *Engine) RunDiscovery(ctx context.Context, sourceID, idemKey string) (IngestResult, error) {
	src, err := e.Store.GetSource(ctx, sourceID)
	if err != nil {
		return IngestResult{}, err
	}
	if !src.Enabled {
		return IngestResult{}, fmt.Errorf("source %s is disabled", sourceID)
	}
	d, err := e.Registry.Discoverer(src)
	if err != nil {
		return IngestResult{}, &ValidationError{Err: err}
	}
	if idemKey == "" {
		idemKey = store.NewID("manual")
	}
	runID := store.NewID("disc")
	now := e.Store.Now()
	res, err := e.Store.DB.ExecContext(ctx, `INSERT INTO discovery_runs(id, source_id, idempotency_key, state, scheduled_at, started_at) VALUES(?,?,?,?,?,?) ON CONFLICT(idempotency_key) DO NOTHING`,
		runID, sourceID, idemKey, string(domain.DiscoveryDiscovering), now.Format(time.RFC3339Nano), now.Format(time.RFC3339Nano))
	if err != nil {
		return IngestResult{}, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return IngestResult{}, nil // this scheduled run already happened
	}
	_ = e.Store.Event(ctx, e.Store.DB, "", "discovery.started", "", "", map[string]any{"run_id": runID, "source_id": sourceID})
	raws, derr := d.Discover(ctx, src)
	var out IngestResult
	if derr == nil {
		out, derr = e.IngestJobs(ctx, sourceID, raws)
	}
	state, msg := domain.DiscoveryCompleted, ""
	if derr != nil {
		state, msg = domain.DiscoveryFailed, derr.Error()
	}
	_, _ = e.Store.DB.ExecContext(ctx, `UPDATE discovery_runs SET state=?, finished_at=?, found=?, new_jobs=?, changed_jobs=?, duplicates=?, error=? WHERE id=?`,
		string(state), e.Store.Now().Format(time.RFC3339Nano), out.Found, out.New, out.Changed, out.Duplicates, nullable(msg), runID)
	_ = e.Store.RecordSourceHealth(ctx, sourceID, derr)
	_ = e.Store.Event(ctx, e.Store.DB, "", "discovery.finished", "", "", map[string]any{"run_id": runID, "source_id": sourceID, "state": state, "found": out.Found, "new": out.New, "error": msg})
	return out, derr
}

func nullable(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// ------------------------------------------------------------------- email intake

// EmailResult reports what an inbound email did.
type EmailResult struct {
	Duplicate      bool          `json:"duplicate"`
	Classification string        `json:"classification"`
	Jobs           *IngestResult `json:"jobs,omitempty"`
	ApplicationID  string        `json:"application_id,omitempty"`
	Verified       bool          `json:"verified"`
	Note           string        `json:"note,omitempty"`
}

// IntakeEmail processes one job-alert or confirmation email. Replaying the same Message-ID
// is a no-op, so a mailbox can be re-scanned safely.
func (e *Engine) IntakeEmail(ctx context.Context, m sources.Email) (EmailResult, error) {
	if m.MessageID == "" {
		return EmailResult{}, &ValidationError{Err: errors.New("message_id is required")}
	}
	parsed := sources.Parse(m)
	res := EmailResult{Classification: parsed.Classification}
	parsedJSON, _ := json.Marshal(parsed)
	fresh, err := e.Store.RecordMessage(ctx, store.InboundMessage{MessageID: m.MessageID, Sender: m.From, Subject: m.Subject,
		ReceivedAt: m.Date.UTC().Format(time.RFC3339), Classification: parsed.Classification, Parsed: parsedJSON})
	if err != nil {
		return res, err
	}
	if !fresh {
		res.Duplicate = true
		return res, nil
	}
	switch parsed.Classification {
	case sources.ClassLinkedInAlert, sources.ClassITviecAlert:
		r, err := e.IngestJobs(ctx, parsed.SourceID, parsed.Jobs)
		res.Jobs = &r
		return res, err
	case sources.ClassLinkedInConfirmation, sources.ClassITviecConfirmation:
		appID, note, err := e.applyConfirmation(ctx, *parsed.Confirmation)
		res.ApplicationID, res.Note, res.Verified = appID, note, err == nil && appID != ""
		return res, err
	case sources.ClassLinkedInViewed:
		_ = e.Store.Event(ctx, e.Store.DB, "", "outcome.application_viewed", "", "", map[string]any{"source": "linkedin", "company": parsed.ViewedBy})
	}
	return res, nil
}

// applyConfirmation turns a platform confirmation email into a verified receipt. LinkedIn
// confirmations carry the job id (exact match); ITviec's carry title + company (canonical
// key match). An application submitted entirely outside this system is still recorded so
// it is never applied to twice.
func (e *Engine) applyConfirmation(ctx context.Context, c sources.Confirmation) (string, string, error) {
	var job domain.Job
	var err error
	if c.ExternalJobID != "" {
		job, err = e.Store.FindJobByExternal(ctx, c.SourceID, c.ExternalJobID)
	} else {
		err = store.ErrNotFound
		if js, ferr := e.Store.FindJobsByCanonicalKey(ctx, normalize.CanonicalKey(c.Company, c.Title)); ferr == nil && len(js) > 0 {
			job, err = js[0], nil
		}
	}
	if errors.Is(err, store.ErrNotFound) {
		ext := c.ExternalJobID
		if ext == "" {
			ext = "confirm-" + normalize.CanonicalKey(c.Company, c.Title)
		}
		raw := domain.RawJob{ExternalJobID: ext, Company: c.Company, Title: c.Title, Partial: true, RawContentType: "application/json"}
		raw.Raw, _ = json.Marshal(c)
		job, _, err = e.Store.IngestJob(ctx, normalize.Normalize(c.SourceID, raw, e.Store.Now()), raw.RawContentType, raw.Raw)
	}
	if err != nil {
		return "", "", err
	}
	app, err := e.Store.ApplicationByLogicalKey(ctx, job.CanonicalKey)
	if errors.Is(err, store.ErrNotFound) {
		snap, serr := e.Store.LatestCandidateSnapshot(ctx)
		if serr != nil {
			return "", "confirmation stored; no candidate snapshot yet, so no application record was created", nil
		}
		app, _, err = e.Store.CreateApplication(ctx, job, snap.ID, nil)
	}
	if err != nil {
		return "", "", err
	}
	if app.State == domain.StateSubmitted {
		return app.ID, "already submitted", e.Store.Tx(ctx, func(tx *sql.Tx) error {
			return e.Store.SaveReceipt(ctx, tx, store.Receipt{ApplicationID: app.ID, Kind: "email_confirmation", ExternalRef: c.MessageID, Evidence: mustJSON(c), Verified: true})
		})
	}
	if _, busy := e.inflight.Load(app.ID); busy || app.State == domain.StateExecuting || app.State == domain.StateVerifying {
		return app.ID, "receipt stored; automated execution in progress will reconcile", e.Store.Tx(ctx, func(tx *sql.Tx) error {
			return e.Store.SaveReceipt(ctx, tx, store.Receipt{ApplicationID: app.ID, Kind: "email_confirmation", ExternalRef: c.MessageID, Evidence: mustJSON(c), Verified: true})
		})
	}
	err = e.Store.Tx(ctx, func(tx *sql.Tx) error {
		if err := e.Store.SaveReceipt(ctx, tx, store.Receipt{ApplicationID: app.ID, Kind: "email_confirmation", ExternalRef: c.MessageID, Evidence: mustJSON(c), Verified: true}); err != nil {
			return err
		}
		if err := e.Store.Transition(ctx, tx, &app, domain.StateVerifying, store.Patch{}, map[string]any{"evidence": "platform confirmation email", "message_id": c.MessageID}); err != nil {
			return err
		}
		out := "confirmed by " + c.SourceID + " email"
		return e.Store.Transition(ctx, tx, &app, domain.StateSubmitted, store.Patch{Submitted: true, Outcome: &out, ClearNextAttempt: true}, map[string]any{"receipt": "email_confirmation"})
	})
	return app.ID, "", err
}

func mustJSON(v any) json.RawMessage {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return b
}

// ------------------------------------------------------------------- task queue glue

func (e *Engine) enqueueMatch(ctx context.Context, tx *sql.Tx, app store.Application, j domain.Job) error {
	snap, err := e.Store.GetCandidateSnapshot(ctx, app.SnapshotID)
	if err != nil {
		return err
	}
	payload := map[string]any{"task": TaskMatch, "application_id": app.ID, "job": j, "snapshot_id": snap.ID, "candidate": snap.Payload}
	_, err = e.Store.Enqueue(ctx, tx, TaskMatch, app.ID, TaskMatch+":"+app.ID+":"+strconv.Itoa(app.Version), payload, 5)
	return err
}

func (e *Engine) enqueuePrepare(ctx context.Context, tx *sql.Tx, app store.Application) error {
	j, err := e.Store.GetJob(ctx, app.JobID)
	if err != nil {
		return err
	}
	snap, err := e.Store.GetCandidateSnapshot(ctx, app.SnapshotID)
	if err != nil {
		return err
	}
	var match json.RawMessage
	if app.MatchID != nil {
		if match, err = e.Store.GetMatch(ctx, *app.MatchID); err != nil {
			return err
		}
	}
	payload := map[string]any{"task": TaskPrepare, "application_id": app.ID, "job": j, "snapshot_id": snap.ID, "candidate": snap.Payload,
		"match": match, "questions": nonFileQuestions(j.Questions)}
	_, err = e.Store.Enqueue(ctx, tx, TaskPrepare, app.ID, TaskPrepare+":"+app.ID+":"+strconv.Itoa(app.Version), payload, 5)
	return err
}

func nonFileQuestions(qs []domain.Question) []domain.Question {
	out := []domain.Question{}
	for _, q := range qs {
		if q.Type != "file" {
			out = append(out, q)
		}
	}
	return out
}
