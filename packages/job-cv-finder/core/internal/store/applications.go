package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/TuanKietTran/llm-toolkit/packages/job-cv-finder/core/internal/domain"
)

// Application is one application workflow instance.
type Application struct {
	ID                 string          `json:"id"`
	JobID              string          `json:"job_id"`
	LogicalKey         string          `json:"logical_key"`
	State              domain.AppState `json:"state"`
	Version            int             `json:"version"`
	SnapshotID         string          `json:"snapshot_id"`
	MatchID            *string         `json:"match_id"`
	SelectedCVID       *string         `json:"selected_cv_id"`
	SelectedCVRevision *int            `json:"selected_cv_revision"`
	Executor           *string         `json:"executor"`
	IdempotencyKey     string          `json:"idempotency_key"`
	Attempts           int             `json:"attempts"`
	NextAttemptAt      *string         `json:"next_attempt_at"`
	LastError          *string         `json:"last_error"`
	ExceptionReason    *string         `json:"exception_reason"`
	Outcome            *string         `json:"outcome"`
	Notes              *string         `json:"notes"`
	CreatedAt          string          `json:"created_at"`
	UpdatedAt          string          `json:"updated_at"`
	SubmittedAt        *string         `json:"submitted_at"`
}

const appCols = `id, job_id, logical_key, state, version, snapshot_id, match_id, selected_cv_id, selected_cv_revision, executor, idempotency_key,
	attempts, next_attempt_at, last_error, exception_reason, outcome, notes, created_at, updated_at, submitted_at`

func scanApp(row interface{ Scan(...any) error }) (Application, error) {
	var a Application
	var match, cv, ex, next, lerr, exc, out, notes, sub sql.NullString
	var rev sql.NullInt64
	var st string
	err := row.Scan(&a.ID, &a.JobID, &a.LogicalKey, &st, &a.Version, &a.SnapshotID, &match, &cv, &rev, &ex, &a.IdempotencyKey,
		&a.Attempts, &next, &lerr, &exc, &out, &notes, &a.CreatedAt, &a.UpdatedAt, &sub)
	if errors.Is(err, sql.ErrNoRows) {
		return a, ErrNotFound
	}
	if err != nil {
		return a, err
	}
	a.State = domain.AppState(st)
	a.MatchID, a.SelectedCVID, a.Executor, a.NextAttemptAt = nullStr(match), nullStr(cv), nullStr(ex), nullStr(next)
	a.LastError, a.ExceptionReason, a.Outcome, a.Notes, a.SubmittedAt = nullStr(lerr), nullStr(exc), nullStr(out), nullStr(notes), nullStr(sub)
	if rev.Valid {
		r := int(rev.Int64)
		a.SelectedCVRevision = &r
	}
	return a, nil
}

// CreateApplication starts a workflow for (job, snapshot). It is idempotent on the job's
// canonical key: the same real job seen on two sources yields one application.
// onCreate runs inside the same transaction (e.g. to enqueue the first task atomically).
func (s *Store) CreateApplication(ctx context.Context, job domain.Job, snapshotID string, onCreate func(tx *sql.Tx, app *Application) error) (Application, bool, error) {
	var app Application
	created := false
	err := s.Tx(ctx, func(tx *sql.Tx) error {
		existing, err := scanApp(tx.QueryRowContext(ctx, `SELECT `+appCols+` FROM applications WHERE logical_key=?`, job.CanonicalKey))
		if err == nil {
			app = existing
			return nil
		}
		if !errors.Is(err, ErrNotFound) {
			return err
		}
		now := ts(s.Now())
		app = Application{ID: NewID("app"), JobID: job.ID, LogicalKey: job.CanonicalKey, State: domain.StateMatching, Version: 1,
			SnapshotID: snapshotID, IdempotencyKey: NewID("idem"), CreatedAt: now, UpdatedAt: now}
		if _, err := tx.ExecContext(ctx, `INSERT INTO applications(id, job_id, logical_key, state, version, snapshot_id, idempotency_key, created_at, updated_at) VALUES(?,?,?,?,?,?,?,?,?)`,
			app.ID, app.JobID, app.LogicalKey, string(app.State), app.Version, app.SnapshotID, app.IdempotencyKey, now, now); err != nil {
			return err
		}
		created = true
		if err := s.Event(ctx, tx, app.ID, "application.created", "", string(app.State), map[string]any{"job_id": job.ID, "snapshot_id": snapshotID}); err != nil {
			return err
		}
		if onCreate != nil {
			return onCreate(tx, &app)
		}
		return nil
	})
	return app, created, err
}

// GetApplication loads one application.
func (s *Store) GetApplication(ctx context.Context, id string) (Application, error) {
	return scanApp(s.DB.QueryRowContext(ctx, `SELECT `+appCols+` FROM applications WHERE id=?`, id))
}

// GetApplicationTx loads one application inside a transaction.
func (s *Store) GetApplicationTx(ctx context.Context, tx *sql.Tx, id string) (Application, error) {
	return scanApp(tx.QueryRowContext(ctx, `SELECT `+appCols+` FROM applications WHERE id=?`, id))
}

// HasExecutionRun reports whether any submission attempt was ever started for the application,
// whatever its outcome. An application that has one must never be restarted automatically.
func (s *Store) HasExecutionRun(ctx context.Context, appID string) (bool, error) {
	var n int
	err := s.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM execution_runs WHERE application_id=?`, appID).Scan(&n)
	return n > 0, err
}

// ListApplications filters by states (empty = all), newest first.
func (s *Store) ListApplications(ctx context.Context, states []domain.AppState, limit int) ([]Application, error) {
	q := `SELECT ` + appCols + ` FROM applications`
	var args []any
	if len(states) > 0 {
		q += ` WHERE state IN (?` + strings.Repeat(",?", len(states)-1) + `)`
		for _, st := range states {
			args = append(args, string(st))
		}
	}
	if limit <= 0 || limit > 1000 {
		limit = 200
	}
	q += ` ORDER BY updated_at DESC LIMIT ?`
	args = append(args, limit)
	rows, err := s.DB.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Application
	for rows.Next() {
		a, err := scanApp(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// Due returns applications in actionable states whose next_attempt_at has passed.
func (s *Store) Due(ctx context.Context, states []domain.AppState, now time.Time, limit int) ([]Application, error) {
	q := `SELECT ` + appCols + ` FROM applications WHERE state IN (?` + strings.Repeat(",?", len(states)-1) + `) AND (next_attempt_at IS NULL OR next_attempt_at <= ?) ORDER BY updated_at LIMIT ?`
	var args []any
	for _, st := range states {
		args = append(args, string(st))
	}
	args = append(args, ts(now), limit)
	rows, err := s.DB.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Application
	for rows.Next() {
		a, err := scanApp(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// Patch lists optional column updates applied together with a transition.
type Patch struct {
	MatchID            *string
	SelectedCVID       *string
	SelectedCVRevision *int
	Executor           *string
	IncAttempts        bool
	NextAttemptAt      *time.Time
	ClearNextAttempt   bool
	LastError          *string
	ExceptionReason    *string
	ClearException     bool
	Outcome            *string
	Notes              *string
	Submitted          bool
}

// Transition moves an application from -> to with optimistic concurrency, applying patch
// and appending an event, all inside tx. Illegal edges are rejected.
func (s *Store) Transition(ctx context.Context, tx *sql.Tx, app *Application, to domain.AppState, p Patch, detail map[string]any) error {
	if err := domain.CheckTransition(app.State, to); err != nil {
		return err
	}
	if to == domain.StateSubmitted {
		// "Submitted" is only ever shown with evidence; refuse the state rather than lose it.
		var n int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM submission_receipts WHERE application_id=?`, app.ID).Scan(&n); err != nil {
			return err
		}
		if n == 0 {
			return fmt.Errorf("refusing to mark %s SUBMITTED without a receipt", app.ID)
		}
	}
	sets := []string{"state=?", "version=version+1", "updated_at=?"}
	now := s.Now()
	args := []any{string(to), ts(now)}
	add := func(col string, v any) { sets = append(sets, col+"=?"); args = append(args, v) }
	if p.MatchID != nil {
		add("match_id", *p.MatchID)
	}
	if p.SelectedCVID != nil {
		add("selected_cv_id", *p.SelectedCVID)
	}
	if p.SelectedCVRevision != nil {
		add("selected_cv_revision", *p.SelectedCVRevision)
	}
	if p.Executor != nil {
		add("executor", *p.Executor)
	}
	if p.IncAttempts {
		sets = append(sets, "attempts=attempts+1")
	}
	if p.NextAttemptAt != nil {
		add("next_attempt_at", ts(*p.NextAttemptAt))
	} else if p.ClearNextAttempt {
		sets = append(sets, "next_attempt_at=NULL")
	}
	if p.LastError != nil {
		add("last_error", *p.LastError)
	}
	if p.ExceptionReason != nil {
		add("exception_reason", *p.ExceptionReason)
	} else if p.ClearException {
		sets = append(sets, "exception_reason=NULL")
	}
	if p.Outcome != nil {
		add("outcome", *p.Outcome)
	}
	if p.Notes != nil {
		add("notes", *p.Notes)
	}
	if p.Submitted {
		add("submitted_at", ts(now))
	}
	args = append(args, app.ID, app.Version, string(app.State))
	res, err := tx.ExecContext(ctx, `UPDATE applications SET `+strings.Join(sets, ", ")+` WHERE id=? AND version=? AND state=?`, args...)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return ErrConflict
	}
	if detail == nil {
		detail = map[string]any{}
	}
	if p.ExceptionReason != nil {
		detail["reason"] = *p.ExceptionReason
	}
	if p.LastError != nil {
		detail["error"] = *p.LastError
	}
	if err := s.Event(ctx, tx, app.ID, "state.changed", string(app.State), string(to), detail); err != nil {
		return err
	}
	fresh, err := s.GetApplicationTx(ctx, tx, app.ID)
	if err != nil {
		return err
	}
	*app = fresh
	return nil
}

// SaveMatch persists a validated match result.
func (s *Store) SaveMatch(ctx context.Context, tx *sql.Tx, jobID, snapshotID string, score float64, eligibility string, confidence float64, rec string, raw []byte) (string, error) {
	id := NewID("match")
	_, err := tx.ExecContext(ctx, `INSERT INTO job_matches(id, job_id, snapshot_id, match_score, eligibility, confidence, recommendation, result, created_at)
		VALUES(?,?,?,?,?,?,?,?,?) ON CONFLICT(job_id, snapshot_id) DO UPDATE SET match_score=excluded.match_score, eligibility=excluded.eligibility,
		confidence=excluded.confidence, recommendation=excluded.recommendation, result=excluded.result, created_at=excluded.created_at`,
		id, jobID, snapshotID, score, eligibility, confidence, rec, string(raw), ts(s.Now()))
	if err != nil {
		return "", err
	}
	err = tx.QueryRowContext(ctx, `SELECT id FROM job_matches WHERE job_id=? AND snapshot_id=?`, jobID, snapshotID).Scan(&id)
	return id, err
}

// GetMatch returns the raw match result JSON.
func (s *Store) GetMatch(ctx context.Context, id string) (json.RawMessage, error) {
	var raw string
	err := s.DB.QueryRowContext(ctx, `SELECT result FROM job_matches WHERE id=?`, id).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return json.RawMessage(raw), err
}

// Document is a generated application artifact.
type Document struct {
	ID               string          `json:"id"`
	ApplicationID    string          `json:"application_id"`
	Kind             string          `json:"kind"`
	Format           string          `json:"format"`
	Content          string          `json:"content"`
	ContentSHA256    string          `json:"content_sha256"`
	Statements       json.RawMessage `json:"statements"`
	SourceCVID       string          `json:"source_cv_id"`
	SourceCVRevision int             `json:"source_cv_revision"`
	JobSnapshotID    string          `json:"job_snapshot_id"`
	Engine           json.RawMessage `json:"engine"`
	Validation       json.RawMessage `json:"validation"`
	RuxtRegistration string          `json:"ruxt_registration"`
	CreatedAt        string          `json:"created_at"`
}

// SaveDocument stores one generated document.
func (s *Store) SaveDocument(ctx context.Context, tx *sql.Tx, d Document) error {
	_, err := tx.ExecContext(ctx, `INSERT INTO application_documents(id, application_id, kind, format, content, content_sha256, statements, source_cv_id, source_cv_revision, job_snapshot_id, engine, validation, ruxt_registration, created_at)
		VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		d.ID, d.ApplicationID, d.Kind, d.Format, d.Content, d.ContentSHA256, string(d.Statements), d.SourceCVID, d.SourceCVRevision, d.JobSnapshotID, string(d.Engine), string(d.Validation), d.RuxtRegistration, ts(s.Now()))
	return err
}

// ListDocuments returns an application's documents (newest generation first).
func (s *Store) ListDocuments(ctx context.Context, appID string) ([]Document, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT id, application_id, kind, format, content, content_sha256, statements, source_cv_id, source_cv_revision, COALESCE(job_snapshot_id,''), engine, validation, ruxt_registration, created_at
		FROM application_documents WHERE application_id=? ORDER BY created_at DESC`, appID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Document
	for rows.Next() {
		var d Document
		var st, eng, val string
		if err := rows.Scan(&d.ID, &d.ApplicationID, &d.Kind, &d.Format, &d.Content, &d.ContentSHA256, &st, &d.SourceCVID, &d.SourceCVRevision, &d.JobSnapshotID, &eng, &val, &d.RuxtRegistration, &d.CreatedAt); err != nil {
			return nil, err
		}
		d.Statements, d.Engine, d.Validation = json.RawMessage(st), json.RawMessage(eng), json.RawMessage(val)
		out = append(out, d)
	}
	return out, rows.Err()
}

// SetDocumentRegistration records whether Ruxt accepted a generated artifact.
func (s *Store) SetDocumentRegistration(ctx context.Context, docID, status string) error {
	_, err := s.DB.ExecContext(ctx, `UPDATE application_documents SET ruxt_registration=? WHERE id=?`, status, docID)
	return err
}

// Answer is one application-question answer.
type Answer struct {
	QuestionID   string   `json:"question_id"`
	Question     string   `json:"question"`
	Required     bool     `json:"required"`
	Status       string   `json:"status"`
	Answer       *string  `json:"answer"`
	Source       string   `json:"source"`
	EvidenceRefs []string `json:"evidence_refs"`
}

// SaveAnswers replaces an application's answers.
func (s *Store) SaveAnswers(ctx context.Context, tx *sql.Tx, appID string, answers []Answer) error {
	if _, err := tx.ExecContext(ctx, `DELETE FROM application_answers WHERE application_id=?`, appID); err != nil {
		return err
	}
	for _, a := range answers {
		if _, err := tx.ExecContext(ctx, `INSERT INTO application_answers(application_id, question_id, question, required, status, answer, source, evidence_refs) VALUES(?,?,?,?,?,?,?,?)`,
			appID, a.QuestionID, a.Question, boolInt(a.Required), a.Status, a.Answer, a.Source, js(nonNil(a.EvidenceRefs))); err != nil {
			return err
		}
	}
	return nil
}

// ListAnswers returns an application's answers.
func (s *Store) ListAnswers(ctx context.Context, appID string) ([]Answer, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT question_id, question, required, status, answer, source, evidence_refs FROM application_answers WHERE application_id=? ORDER BY question_id`, appID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Answer
	for rows.Next() {
		var a Answer
		var req int
		var ans sql.NullString
		var refs string
		if err := rows.Scan(&a.QuestionID, &a.Question, &req, &a.Status, &ans, &a.Source, &refs); err != nil {
			return nil, err
		}
		a.Required = req == 1
		a.Answer = nullStr(ans)
		_ = json.Unmarshal([]byte(refs), &a.EvidenceRefs)
		out = append(out, a)
	}
	return out, rows.Err()
}

// SavePolicyDecision records a policy outcome.
func (s *Store) SavePolicyDecision(ctx context.Context, tx *sql.Tx, appID string, d domain.PolicyDecision) error {
	_, err := tx.ExecContext(ctx, `INSERT INTO policy_decisions(id, application_id, outcome, reasons, rules_version, created_at) VALUES(?,?,?,?,?,?)`,
		NewID("pol"), appID, d.Outcome, js(d.Reasons), d.Rules, ts(s.Now()))
	return err
}

// ListPolicyDecisions returns decisions for an application, newest first.
func (s *Store) ListPolicyDecisions(ctx context.Context, appID string) ([]map[string]any, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT outcome, reasons, rules_version, created_at FROM policy_decisions WHERE application_id=? ORDER BY created_at DESC`, appID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []map[string]any
	for rows.Next() {
		var o, r, v, c string
		if err := rows.Scan(&o, &r, &v, &c); err != nil {
			return nil, err
		}
		var reasons []string
		_ = json.Unmarshal([]byte(r), &reasons)
		out = append(out, map[string]any{"outcome": o, "reasons": reasons, "rules_version": v, "created_at": c})
	}
	return out, rows.Err()
}

// Run is one executor attempt.
type Run struct {
	ID             string  `json:"id"`
	ApplicationID  string  `json:"application_id"`
	Executor       string  `json:"executor"`
	IdempotencyKey string  `json:"idempotency_key"`
	Attempt        int     `json:"attempt"`
	State          string  `json:"state"`
	StartedAt      string  `json:"started_at"`
	FinishedAt     *string `json:"finished_at"`
	Error          *string `json:"error"`
	Response       *string `json:"response"`
}

// StartRun records the start of an attempt BEFORE the external call (write-ahead), so a
// crash mid-call is detectable as a run stuck in "started".
func (s *Store) StartRun(ctx context.Context, tx *sql.Tx, appID, executor, idem string, attempt int) (string, error) {
	id := NewID("run")
	_, err := tx.ExecContext(ctx, `INSERT INTO execution_runs(id, application_id, executor, idempotency_key, attempt, state, started_at) VALUES(?,?,?,?,?,?,?)`,
		id, appID, executor, idem, attempt, "started", ts(s.Now()))
	return id, err
}

// FinishRun closes an attempt.
func (s *Store) FinishRun(ctx context.Context, x execer, runID, state, errMsg string, response any) error {
	var e any
	if errMsg != "" {
		e = errMsg
	}
	_, err := x.ExecContext(ctx, `UPDATE execution_runs SET state=?, finished_at=?, error=?, response=? WHERE id=?`, state, ts(s.Now()), e, js(response), runID)
	return err
}

// ListRuns returns an application's runs.
func (s *Store) ListRuns(ctx context.Context, appID string) ([]Run, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT id, application_id, executor, idempotency_key, attempt, state, started_at, finished_at, error, response FROM execution_runs WHERE application_id=? ORDER BY attempt`, appID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Run
	for rows.Next() {
		var r Run
		var fin, e, resp sql.NullString
		if err := rows.Scan(&r.ID, &r.ApplicationID, &r.Executor, &r.IdempotencyKey, &r.Attempt, &r.State, &r.StartedAt, &fin, &e, &resp); err != nil {
			return nil, err
		}
		r.FinishedAt, r.Error, r.Response = nullStr(fin), nullStr(e), nullStr(resp)
		out = append(out, r)
	}
	return out, rows.Err()
}

// Receipt is evidence that a submission happened.
type Receipt struct {
	ID            string          `json:"id"`
	ApplicationID string          `json:"application_id"`
	Kind          string          `json:"kind"`
	ExternalRef   string          `json:"external_ref"`
	Evidence      json.RawMessage `json:"evidence"`
	Verified      bool            `json:"verified"`
	ReceivedAt    string          `json:"received_at"`
}

// SaveReceipt stores a receipt. Replaying the same receipt for the same application is a no-op.
// If a different application already holds the (kind, external_ref) pair, the platform reused a
// reference (or a test double restarted); the submission still happened, so the receipt is kept
// under a disambiguated reference instead of being dropped, which would have left the
// application SUBMITTED with no receipt.
func (s *Store) SaveReceipt(ctx context.Context, tx *sql.Tx, r Receipt) error {
	ref := r.ExternalRef
	for attempt := 0; attempt < 2; attempt++ {
		res, err := tx.ExecContext(ctx, `INSERT INTO submission_receipts(id, application_id, kind, external_ref, evidence, verified, received_at) VALUES(?,?,?,?,?,?,?)
			ON CONFLICT(kind, external_ref) DO NOTHING`,
			NewID("rcpt"), r.ApplicationID, r.Kind, ref, string(r.Evidence), boolInt(r.Verified), ts(s.Now()))
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n > 0 {
			return nil
		}
		var owner string
		if err := tx.QueryRowContext(ctx, `SELECT application_id FROM submission_receipts WHERE kind=? AND external_ref=?`, r.Kind, ref).Scan(&owner); err != nil {
			return err
		}
		if owner == r.ApplicationID {
			return nil
		}
		ref = r.ExternalRef + "#" + r.ApplicationID
	}
	return fmt.Errorf("receipt %s/%s could not be stored", r.Kind, r.ExternalRef)
}

// ListReceipts returns receipts for an application.
func (s *Store) ListReceipts(ctx context.Context, appID string) ([]Receipt, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT id, application_id, kind, external_ref, evidence, verified, received_at FROM submission_receipts WHERE application_id=? ORDER BY received_at`, appID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Receipt
	for rows.Next() {
		var r Receipt
		var ev string
		var v int
		if err := rows.Scan(&r.ID, &r.ApplicationID, &r.Kind, &r.ExternalRef, &ev, &v, &r.ReceivedAt); err != nil {
			return nil, err
		}
		r.Evidence, r.Verified = json.RawMessage(ev), v == 1
		out = append(out, r)
	}
	return out, rows.Err()
}

// CountSubmittedSince counts verified submissions for a source since t (daily limits).
func (s *Store) CountSubmittedSince(ctx context.Context, sourceID string, t time.Time) (int, error) {
	var n int
	err := s.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM applications a JOIN jobs j ON j.id=a.job_id WHERE j.source_id=? AND a.executor IS NOT NULL AND a.executor!='handoff' AND a.state IN ('EXECUTING','VERIFYING','SUBMITTED','SUBMISSION_UNKNOWN') AND a.updated_at>=?`,
		sourceID, ts(t)).Scan(&n)
	return n, err
}

// EventRow is one entry of the execution history.
type EventRow struct {
	Seq           int64           `json:"seq"`
	ApplicationID *string         `json:"application_id"`
	Kind          string          `json:"kind"`
	FromState     *string         `json:"from_state"`
	ToState       *string         `json:"to_state"`
	Detail        json.RawMessage `json:"detail"`
	CreatedAt     string          `json:"created_at"`
}

// ListEvents returns events after seq (optionally for one application), oldest first.
func (s *Store) ListEvents(ctx context.Context, afterSeq int64, appID string, limit int) ([]EventRow, error) {
	if limit <= 0 || limit > 1000 {
		limit = 200
	}
	q := `SELECT seq, application_id, kind, from_state, to_state, detail, created_at FROM execution_events WHERE seq>?`
	args := []any{afterSeq}
	if appID != "" {
		q += ` AND application_id=?`
		args = append(args, appID)
	}
	q += ` ORDER BY seq LIMIT ?`
	args = append(args, limit)
	rows, err := s.DB.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []EventRow
	for rows.Next() {
		var e EventRow
		var app, from, to sql.NullString
		var d string
		if err := rows.Scan(&e.Seq, &app, &e.Kind, &from, &to, &d, &e.CreatedAt); err != nil {
			return nil, err
		}
		e.ApplicationID, e.FromState, e.ToState, e.Detail = nullStr(app), nullStr(from), nullStr(to), json.RawMessage(d)
		out = append(out, e)
	}
	return out, rows.Err()
}

// Counts returns the number of applications per state, plus job totals, for the overview.
func (s *Store) Counts(ctx context.Context) (map[string]int, error) {
	out := map[string]int{}
	rows, err := s.DB.QueryContext(ctx, `SELECT state, COUNT(*) FROM applications GROUP BY state`)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var st string
		var n int
		if err := rows.Scan(&st, &n); err != nil {
			rows.Close()
			return nil, err
		}
		out["applications."+st] = n
	}
	rows.Close()
	var total, primary, fresh int
	day := ts(s.Now().Add(-24 * time.Hour))
	if err := s.DB.QueryRowContext(ctx, `SELECT COUNT(*), SUM(CASE WHEN duplicate_of IS NULL THEN 1 ELSE 0 END), SUM(CASE WHEN collected_at>=? THEN 1 ELSE 0 END) FROM jobs`, day).Scan(&total, nullInt(&primary), nullInt(&fresh)); err != nil {
		return nil, err
	}
	out["jobs.total"], out["jobs.unique"], out["jobs.new_24h"] = total, primary, fresh
	return out, nil
}

type nullIntScanner struct{ p *int }

func (n nullIntScanner) Scan(v any) error {
	switch x := v.(type) {
	case nil:
		*n.p = 0
	case int64:
		*n.p = int(x)
	case float64:
		*n.p = int(x)
	}
	return nil
}

func nullInt(p *int) nullIntScanner { return nullIntScanner{p} }

// Defer postpones the next automatic step without changing state or version.
func (s *Store) Defer(ctx context.Context, app Application, until time.Time, lastErr string) error {
	var e any
	if lastErr != "" {
		e = lastErr
	}
	_, err := s.DB.ExecContext(ctx, `UPDATE applications SET next_attempt_at=?, last_error=COALESCE(?, last_error) WHERE id=? AND version=?`, ts(until), e, app.ID, app.Version)
	return err
}

// ApplicationByLogicalKey finds the application for a real job.
func (s *Store) ApplicationByLogicalKey(ctx context.Context, key string) (Application, error) {
	return scanApp(s.DB.QueryRowContext(ctx, `SELECT `+appCols+` FROM applications WHERE logical_key=?`, key))
}
