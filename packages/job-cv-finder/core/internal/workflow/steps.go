package workflow

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/TuanKietTran/llm-toolkit/packages/job-cv-finder/core/contracts"
	"github.com/TuanKietTran/llm-toolkit/packages/job-cv-finder/core/internal/domain"
	"github.com/TuanKietTran/llm-toolkit/packages/job-cv-finder/core/internal/execution"
	"github.com/TuanKietTran/llm-toolkit/packages/job-cv-finder/core/internal/policy"
	"github.com/TuanKietTran/llm-toolkit/packages/job-cv-finder/core/internal/store"
)

// actionable states are polled by the loop; HANDOFF_PENDING, BLOCKED_EXCEPTION and the
// terminal states wait for external evidence or the candidate.
// Greenhouse-style boards expose each attachment slot twice: a file field and a paste-as-text
// alternative, both flagged required. The slot is satisfied once its document is attached;
// demanding the text twin as well would block every such application until the candidate pasted
// their resume into a form.
var (
	attachmentKinds     = map[string]string{"resume": "tailored_cv", "cover_letter": "cover_letter"}
	attachmentTextTwins = map[string]string{"resume_text": "resume", "cover_letter_text": "cover_letter"}
)

var actionable = []domain.AppState{
	domain.StateMatching, domain.StateMatched, domain.StateSelected, domain.StatePreparing, domain.StatePrepared,
	domain.StatePolicyCheck, domain.StateExecuting, domain.StateVerifying, domain.StateFailedRetryable, domain.StateSubmissionUnknown,
}

// Tick runs one scheduler pass: reclaim expired task leases, surface dead tasks, and step
// every due application.
func (e *Engine) Tick(ctx context.Context) error {
	if err := e.Store.ReclaimExpired(ctx); err != nil {
		return err
	}
	if err := e.surfaceDeadTasks(ctx); err != nil {
		return err
	}
	apps, err := e.Store.Due(ctx, actionable, e.Store.Now(), 100)
	if err != nil {
		return err
	}
	for _, a := range apps {
		if err := e.Step(ctx, a.ID); err != nil && !errors.Is(err, store.ErrConflict) {
			e.Log.Error("step failed", "application", a.ID, "state", a.State, "err", err)
		}
	}
	return nil
}

// Run loops until ctx is cancelled: ticks, plus discovery for polling sources.
func (e *Engine) Run(ctx context.Context) {
	t := time.NewTicker(e.Cfg.TickEvery)
	defer t.Stop()
	lastDiscovery := time.Time{}
	for {
		if err := e.Tick(ctx); err != nil && ctx.Err() == nil {
			e.Log.Error("tick failed", "err", err)
		}
		if time.Since(lastDiscovery) >= e.Cfg.DiscoverEvery {
			lastDiscovery = time.Now()
			e.discoverAll(ctx)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		case <-e.wake:
		}
	}
}

func (e *Engine) discoverAll(ctx context.Context) {
	srcs, err := e.Store.ListSources(ctx)
	if err != nil {
		e.Log.Error("list sources", "err", err)
		return
	}
	bucket := e.Store.Now().Truncate(e.Cfg.DiscoverEvery).Format(time.RFC3339)
	for _, s := range srcs {
		if !s.Enabled {
			continue
		}
		if _, err := e.Registry.Discoverer(s); err != nil {
			continue // not a polling source (email intake / user provided)
		}
		if _, err := e.RunDiscovery(ctx, s.ID, "sched:"+s.ID+":"+bucket); err != nil {
			e.Log.Warn("discovery failed", "source", s.ID, "err", err)
		}
	}
}

func (e *Engine) surfaceDeadTasks(ctx context.Context) error {
	for _, kind := range []string{TaskMatch, TaskPrepare} {
		dead, err := e.Store.DeadTasks(ctx, kind)
		if err != nil {
			return err
		}
		for _, t := range dead {
			if t.ApplicationID == nil {
				continue
			}
			app, err := e.Store.GetApplication(ctx, *t.ApplicationID)
			if err != nil {
				return err
			}
			waiting := (kind == TaskMatch && app.State == domain.StateMatching) || (kind == TaskPrepare && app.State == domain.StatePreparing)
			if !waiting || !strings.HasSuffix(t.IdempotencyKey, ":"+fmt.Sprint(app.Version)) {
				continue
			}
			reason := fmt.Sprintf("intelligence task %s failed permanently: %s", kind, derefS(t.Error))
			_ = e.Store.Tx(ctx, func(tx *sql.Tx) error {
				return e.Store.Transition(ctx, tx, &app, domain.StateBlockedException, store.Patch{ExceptionReason: &reason}, nil)
			})
		}
	}
	return nil
}

func derefS(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

// Step advances one application by at most one external action.
func (e *Engine) Step(ctx context.Context, appID string) error {
	app, err := e.Store.GetApplication(ctx, appID)
	if err != nil {
		return err
	}
	switch app.State {
	case domain.StateMatching:
		return e.reassertTask(ctx, app, TaskMatch)
	case domain.StatePreparing:
		return e.reassertTask(ctx, app, TaskPrepare)
	case domain.StateMatched:
		return e.selectStep(ctx, app)
	case domain.StateSelected:
		return e.Store.Tx(ctx, func(tx *sql.Tx) error {
			if err := e.Store.Transition(ctx, tx, &app, domain.StatePreparing, store.Patch{ClearNextAttempt: true}, nil); err != nil {
				return err
			}
			return e.enqueuePrepare(ctx, tx, app)
		})
	case domain.StatePrepared:
		if err := e.Store.Tx(ctx, func(tx *sql.Tx) error {
			return e.Store.Transition(ctx, tx, &app, domain.StatePolicyCheck, store.Patch{}, nil)
		}); err != nil {
			return err
		}
		return e.policyStep(ctx, app)
	case domain.StatePolicyCheck:
		return e.policyStep(ctx, app)
	case domain.StateExecuting:
		return e.recoverExecuting(ctx, app)
	case domain.StateVerifying:
		return e.verifyStep(ctx, app)
	case domain.StateSubmissionUnknown:
		return e.reconcileStep(ctx, app)
	case domain.StateFailedRetryable:
		return e.Store.Tx(ctx, func(tx *sql.Tx) error {
			if app.Attempts >= e.Cfg.MaxExecAttempts {
				msg := fmt.Sprintf("gave up after %d attempts", app.Attempts)
				return e.Store.Transition(ctx, tx, &app, domain.StateFailedFinal, store.Patch{LastError: &msg}, nil)
			}
			return e.Store.Transition(ctx, tx, &app, domain.StatePolicyCheck, store.Patch{ClearNextAttempt: true}, map[string]any{"retry": app.Attempts + 1})
		})
	}
	return nil
}

func (e *Engine) reassertTask(ctx context.Context, app store.Application, kind string) error {
	err := e.Store.Tx(ctx, func(tx *sql.Tx) error {
		if kind == TaskMatch {
			j, err := e.Store.GetJob(ctx, app.JobID)
			if err != nil {
				return err
			}
			return e.enqueueMatch(ctx, tx, app, j)
		}
		return e.enqueuePrepare(ctx, tx, app)
	})
	if err != nil {
		return err
	}
	return e.Store.Defer(ctx, app, e.Store.Now().Add(e.Cfg.WaitRecheck), "")
}

func (e *Engine) selectStep(ctx context.Context, app store.Application) error {
	if app.MatchID == nil {
		return fmt.Errorf("application %s is MATCHED without a match", app.ID)
	}
	raw, err := e.Store.GetMatch(ctx, *app.MatchID)
	if err != nil {
		return err
	}
	var m struct {
		Recommendation string  `json:"recommendation"`
		Eligibility    string  `json:"eligibility"`
		Score          float64 `json:"match_score"`
	}
	if err := json.Unmarshal(raw, &m); err != nil {
		return err
	}
	to := domain.StateNotSelected
	if (m.Recommendation == "strong_match" || m.Recommendation == "match") && m.Eligibility != "ineligible" {
		to = domain.StateSelected
	}
	return e.Store.Tx(ctx, func(tx *sql.Tx) error {
		return e.Store.Transition(ctx, tx, &app, to, store.Patch{}, map[string]any{"recommendation": m.Recommendation, "eligibility": m.Eligibility, "score": m.Score})
	})
}

// ---------------------------------------------------------------- task completion

// TaskError is a worker-reported or contract failure.
type TaskError struct{ Err error }

func (t *TaskError) Error() string { return t.Err.Error() }

// CompleteTask validates a worker result against its contract and applies it. A result
// for an application that has moved on is accepted (the task is done) but ignored.
func (e *Engine) CompleteTask(ctx context.Context, taskID, worker string, result []byte) error {
	t, err := e.Store.GetTask(ctx, taskID)
	if err != nil {
		return err
	}
	schema := map[string]string{TaskMatch: contracts.MatchResult, TaskPrepare: contracts.PrepareResult}[t.Kind]
	if schema == "" {
		return &ValidationError{Err: fmt.Errorf("unknown task kind %s", t.Kind)}
	}
	if err := contracts.Validate(schema, result); err != nil {
		// A contract violation is a worker bug: fail the attempt (it will be retried, then dead-lettered).
		_, ferr := e.Store.FailTask(ctx, taskID, worker, "contract violation: "+err.Error(), true, e.onDeadTask(ctx))
		if ferr != nil {
			return ferr
		}
		return &ValidationError{Err: err}
	}
	err = e.Store.CompleteTask(ctx, taskID, worker, result, func(tx *sql.Tx, t store.Task) error {
		app, err := e.Store.GetApplicationTx(ctx, tx, derefS(t.ApplicationID))
		if err != nil {
			return err
		}
		want := map[string]domain.AppState{TaskMatch: domain.StateMatching, TaskPrepare: domain.StatePreparing}[t.Kind]
		if app.State != want || !strings.HasSuffix(t.IdempotencyKey, ":"+fmt.Sprint(app.Version)) {
			return e.Store.Event(ctx, tx, app.ID, "task.result_ignored", "", "", map[string]any{"task_id": t.ID, "state": app.State})
		}
		if t.Kind == TaskMatch {
			return e.applyMatch(ctx, tx, &app, result)
		}
		return e.applyPrepare(ctx, tx, &app, result)
	})
	var te *TaskError
	if errors.As(err, &te) {
		// Schema-valid but semantically wrong (wrong ids, foreign evidence): count as a failed attempt.
		if _, ferr := e.Store.FailTask(ctx, taskID, worker, "rejected result: "+te.Error(), true, e.onDeadTask(ctx)); ferr != nil {
			return ferr
		}
		return &ValidationError{Err: te}
	}
	if err == nil {
		e.Wake()
	}
	return err
}

// FailTask records a worker-reported failure.
func (e *Engine) FailTask(ctx context.Context, taskID, worker, msg string, retryable bool) (string, error) {
	return e.Store.FailTask(ctx, taskID, worker, msg, retryable, e.onDeadTask(ctx))
}

func (e *Engine) onDeadTask(ctx context.Context) func(tx *sql.Tx, t store.Task) error {
	return func(tx *sql.Tx, t store.Task) error {
		app, err := e.Store.GetApplicationTx(ctx, tx, derefS(t.ApplicationID))
		if err != nil {
			return err
		}
		if app.State != domain.StateMatching && app.State != domain.StatePreparing {
			return nil
		}
		reason := fmt.Sprintf("intelligence task %s failed permanently: %s", t.Kind, derefS(t.Error))
		return e.Store.Transition(ctx, tx, &app, domain.StateBlockedException, store.Patch{ExceptionReason: &reason}, nil)
	}
}

func (e *Engine) applyMatch(ctx context.Context, tx *sql.Tx, app *store.Application, raw []byte) error {
	var m struct {
		JobID          string       `json:"job_id"`
		SnapshotID     string       `json:"snapshot_id"`
		Score          float64      `json:"match_score"`
		Eligibility    string       `json:"eligibility"`
		Confidence     float64      `json:"confidence"`
		Recommendation string       `json:"recommendation"`
		Facts          []store.Fact `json:"facts"`
	}
	if err := json.Unmarshal(raw, &m); err != nil {
		return err
	}
	if m.JobID != app.JobID || m.SnapshotID != app.SnapshotID {
		return &TaskError{Err: fmt.Errorf("match result is for job %s/snapshot %s, expected %s/%s", m.JobID, m.SnapshotID, app.JobID, app.SnapshotID)}
	}
	if err := e.checkFactSources(ctx, app.SnapshotID, m.Facts); err != nil {
		return &TaskError{Err: err}
	}
	if err := e.Store.SaveFacts(ctx, tx, app.SnapshotID, m.Facts); err != nil {
		return err
	}
	id, err := e.Store.SaveMatch(ctx, tx, app.JobID, app.SnapshotID, m.Score, m.Eligibility, m.Confidence, m.Recommendation, raw)
	if err != nil {
		return err
	}
	return e.Store.Transition(ctx, tx, app, domain.StateMatched, store.Patch{MatchID: &id, ClearNextAttempt: true}, map[string]any{"score": m.Score, "recommendation": m.Recommendation})
}

// checkFactSources rejects facts citing documents that are not in the snapshot: evidence
// must point at the exact Ruxt CV revision (or the profile) that was sent.
func (e *Engine) checkFactSources(ctx context.Context, snapshotID string, facts []store.Fact) error {
	snap, err := e.Store.GetCandidateSnapshot(ctx, snapshotID)
	if err != nil {
		return err
	}
	valid := map[string]string{"profile:" + snap.RuxtProfileID: snap.ProfileUpdatedAt}
	for _, cv := range snap.CVs {
		valid["cv:"+cv.CVID] = fmt.Sprint(cv.Revision)
	}
	for _, f := range facts {
		ver, ok := valid[f.SourceDocumentID]
		if !ok {
			return fmt.Errorf("fact %s cites unknown document %s", f.FactID, f.SourceDocumentID)
		}
		if ver != f.SourceVersionID {
			return fmt.Errorf("fact %s cites %s version %s, snapshot has %s", f.FactID, f.SourceDocumentID, f.SourceVersionID, ver)
		}
	}
	return nil
}

func (e *Engine) applyPrepare(ctx context.Context, tx *sql.Tx, app *store.Application, raw []byte) error {
	var p struct {
		ApplicationID string `json:"application_id"`
		SelectedCV    struct {
			CVID     string `json:"cv_id"`
			Revision int    `json:"revision"`
		} `json:"selected_cv"`
		Documents []struct {
			Kind       string `json:"kind"`
			Format     string `json:"format"`
			Content    string `json:"content"`
			Statements []struct {
				Text         string   `json:"text"`
				EvidenceRefs []string `json:"evidence_refs"`
			} `json:"statements"`
			SourceCVID       string `json:"source_cv_id"`
			SourceCVRevision int    `json:"source_cv_revision"`
		} `json:"documents"`
		Answers []struct {
			QuestionID   string   `json:"question_id"`
			Status       string   `json:"status"`
			Answer       *string  `json:"answer"`
			EvidenceRefs []string `json:"evidence_refs"`
			Source       string   `json:"source"`
		} `json:"answers"`
		Validation json.RawMessage `json:"validation"`
		Engine     json.RawMessage `json:"engine"`
	}
	if err := json.Unmarshal(raw, &p); err != nil {
		return err
	}
	if p.ApplicationID != app.ID {
		return &TaskError{Err: fmt.Errorf("prepare result for %s, expected %s", p.ApplicationID, app.ID)}
	}
	snap, err := e.Store.GetCandidateSnapshot(ctx, app.SnapshotID)
	if err != nil {
		return err
	}
	revOf := map[string]int{}
	for _, cv := range snap.CVs {
		revOf[cv.CVID] = cv.Revision
	}
	if rev, ok := revOf[p.SelectedCV.CVID]; !ok || rev != p.SelectedCV.Revision {
		return &TaskError{Err: fmt.Errorf("selected CV %s@%d is not in the candidate snapshot", p.SelectedCV.CVID, p.SelectedCV.Revision)}
	}
	facts, err := e.factIDsTx(ctx, tx, app.SnapshotID)
	if err != nil {
		return err
	}
	job, err := e.Store.GetJob(ctx, app.JobID)
	if err != nil {
		return err
	}

	// Independent Go-side evidence check: the worker's own "passed" flag is necessary, not sufficient.
	var issues []string
	var workerVal struct {
		Passed bool `json:"passed"`
	}
	_ = json.Unmarshal(p.Validation, &workerVal)
	if !workerVal.Passed {
		issues = append(issues, "worker validation failed")
	}
	docKinds := map[string]bool{}
	for _, d := range p.Documents {
		if rev, ok := revOf[d.SourceCVID]; !ok || rev != d.SourceCVRevision {
			return &TaskError{Err: fmt.Errorf("%s cites CV %s@%d not in snapshot", d.Kind, d.SourceCVID, d.SourceCVRevision)}
		}
		docIssues := []string{}
		for _, st := range d.Statements {
			if len(st.EvidenceRefs) == 0 {
				docIssues = append(docIssues, "statement without evidence: "+truncate(st.Text, 80))
			}
			for _, r := range st.EvidenceRefs {
				if !facts[r] {
					docIssues = append(docIssues, "unknown evidence ref "+r)
				}
			}
		}
		passed := workerVal.Passed && len(docIssues) == 0
		issues = append(issues, docIssues...)
		val := mustJSON(map[string]any{"passed": passed, "issues": docIssues, "worker": json.RawMessage(p.Validation)})
		sum := sha256.Sum256([]byte(d.Content))
		if err := e.Store.SaveDocument(ctx, tx, store.Document{ID: store.NewID("doc"), ApplicationID: app.ID, Kind: d.Kind, Format: d.Format, Content: d.Content,
			ContentSHA256: hex.EncodeToString(sum[:]), Statements: mustJSON(d.Statements), SourceCVID: d.SourceCVID, SourceCVRevision: d.SourceCVRevision,
			JobSnapshotID: job.SourceSnapshotID, Engine: p.Engine, Validation: val, RuxtRegistration: "pending"}); err != nil {
			return err
		}
		if passed {
			docKinds[d.Kind] = true
		}
	}

	// Answers: file questions are satisfied by documents; text questions come from the worker.
	byID := map[string]int{}
	for i, a := range p.Answers {
		byID[a.QuestionID] = i
	}
	fileQuestions := map[string]bool{}
	for _, q := range job.Questions {
		if q.Type == "file" {
			fileQuestions[q.ID] = true
		}
	}
	var answers []store.Answer
	var missing []string
	for _, q := range job.Questions {
		a := store.Answer{QuestionID: q.ID, Question: q.Label, Required: q.Required, Status: "unknown", Source: "none", EvidenceRefs: []string{}}
		twin, isTwin := attachmentTextTwins[q.ID]
		if q.Type == "file" {
			kind := attachmentKinds[q.ID]
			if docKinds[kind] {
				v := "attached: " + kind
				a.Status, a.Answer, a.Source = "answered", &v, "candidate_fact"
			}
		} else if isTwin && fileQuestions[twin] && docKinds[attachmentKinds[twin]] {
			// The paste-as-text alternative of an attachment slot that is already filled by a file.
			v := "see attached " + attachmentKinds[twin]
			a.Status, a.Answer, a.Source = "answered", &v, "candidate_fact"
		} else if i, ok := byID[q.ID]; ok {
			w := p.Answers[i]
			okRefs := true
			for _, r := range w.EvidenceRefs {
				okRefs = okRefs && facts[r]
			}
			if w.Status == "answered" && w.Answer != nil && strings.TrimSpace(*w.Answer) != "" && okRefs && (w.Source == "candidate_preference" || len(w.EvidenceRefs) > 0) {
				a.Status, a.Answer, a.Source, a.EvidenceRefs = "answered", w.Answer, w.Source, w.EvidenceRefs
			}
		}
		if a.Required && a.Status != "answered" {
			missing = append(missing, q.Label)
		}
		answers = append(answers, a)
	}
	if err := e.Store.SaveAnswers(ctx, tx, app.ID, answers); err != nil {
		return err
	}
	cvID, rev := p.SelectedCV.CVID, p.SelectedCV.Revision
	patch := store.Patch{SelectedCVID: &cvID, SelectedCVRevision: &rev, ClearNextAttempt: true}
	switch {
	case len(issues) > 0:
		reason := "generated documents failed evidence validation: " + strings.Join(issues, "; ")
		patch.ExceptionReason = &reason
		return e.Store.Transition(ctx, tx, app, domain.StateBlockedException, patch, nil)
	case len(missing) > 0:
		reason := "missing information the candidate must provide: " + strings.Join(missing, "; ")
		patch.ExceptionReason = &reason
		return e.Store.Transition(ctx, tx, app, domain.StateBlockedException, patch, map[string]any{"missing": missing})
	}
	patch.ClearException = true
	return e.Store.Transition(ctx, tx, app, domain.StatePrepared, patch, map[string]any{"selected_cv": cvID, "revision": rev})
}

func (e *Engine) factIDsTx(ctx context.Context, tx *sql.Tx, snapshotID string) (map[string]bool, error) {
	rows, err := tx.QueryContext(ctx, `SELECT fact_id FROM candidate_facts WHERE snapshot_id=?`, snapshotID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]bool{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out[id] = true
	}
	return out, rows.Err()
}

func truncate(s string, n int) string {
	if len(s) > n {
		return s[:n] + "…"
	}
	return s
}

// ------------------------------------------------------------- policy & execution

func (e *Engine) policyInput(ctx context.Context, app store.Application) (policy.Input, error) {
	var in policy.Input
	job, err := e.Store.GetJob(ctx, app.JobID)
	if err != nil {
		return in, err
	}
	src, err := e.Store.GetSource(ctx, job.SourceID)
	if err != nil {
		return in, err
	}
	snap, err := e.Store.GetCandidateSnapshot(ctx, app.SnapshotID)
	if err != nil {
		return in, err
	}
	in = policy.Input{Job: job, Source: src, Now: e.Store.Now(), ExecutorAvailable: map[domain.AccessMethod]bool{}}
	if t, err := time.Parse(time.RFC3339Nano, snap.ReceivedAt); err == nil {
		in.SnapshotReceived = t
	}
	if acct, err := e.Store.GetAccountBySource(ctx, src.ID); err == nil {
		in.Account = &acct
	}
	if app.MatchID != nil {
		raw, err := e.Store.GetMatch(ctx, *app.MatchID)
		if err != nil {
			return in, err
		}
		var m struct {
			Recommendation string `json:"recommendation"`
			Eligibility    string `json:"eligibility"`
		}
		_ = json.Unmarshal(raw, &m)
		in.Recommendation, in.Eligibility = m.Recommendation, m.Eligibility
		if app.Notes != nil && strings.Contains(*app.Notes, "override:select") {
			in.Recommendation = "override"
		}
	}
	docs, err := e.Store.ListDocuments(ctx, app.ID)
	if err != nil {
		return in, err
	}
	in.Documents = latestDocs(docs)
	if in.Answers, err = e.Store.ListAnswers(ctx, app.ID); err != nil {
		return in, err
	}
	if in.Receipts, err = e.Store.ListReceipts(ctx, app.ID); err != nil {
		return in, err
	}
	day := e.Store.Now().Truncate(24 * time.Hour)
	if in.SubmittedToday, err = e.Store.CountSubmittedSince(ctx, src.ID, day); err != nil {
		return in, err
	}
	for m := range e.Executors {
		in.ExecutorAvailable[m] = true
	}
	return in, nil
}

// latestDocs keeps the newest document of each kind (regeneration keeps history).
func latestDocs(docs []store.Document) []store.Document {
	seen := map[string]bool{}
	var out []store.Document
	for _, d := range docs { // already newest first
		if !seen[d.Kind] {
			seen[d.Kind] = true
			out = append(out, d)
		}
	}
	return out
}

func (e *Engine) policyStep(ctx context.Context, app store.Application) error {
	in, err := e.policyInput(ctx, app)
	if err != nil {
		return err
	}
	d := policy.Evaluate(in, e.Cfg.Policy)
	var runID string
	var exec execution.Executor
	err = e.Store.Tx(ctx, func(tx *sql.Tx) error {
		if err := e.Store.SavePolicyDecision(ctx, tx, app.ID, d.PolicyDecision); err != nil {
			return err
		}
		detail := map[string]any{"policy": d.Outcome, "reasons": d.Reasons}
		switch d.Outcome {
		case domain.PolicyBlock:
			reason := strings.Join(d.Reasons, "; ")
			return e.Store.Transition(ctx, tx, &app, domain.StateBlockedException, store.Patch{ExceptionReason: &reason}, detail)
		case domain.PolicyHandoff:
			name := execution.HandoffName
			return e.Store.Transition(ctx, tx, &app, domain.StateHandoffPending, store.Patch{Executor: &name, ClearException: true}, detail)
		}
		exec = e.Executors[d.Method]
		name := exec.Name()
		if err := e.Store.Transition(ctx, tx, &app, domain.StateExecuting, store.Patch{Executor: &name, IncAttempts: true, ClearException: true}, detail); err != nil {
			return err
		}
		// Write-ahead record: committed BEFORE the external call.
		runID, err = e.Store.StartRun(ctx, tx, app.ID, name, app.IdempotencyKey, app.Attempts)
		return err
	})
	if err != nil || exec == nil {
		return err
	}
	return e.execute(ctx, app, in, exec, runID)
}

func (e *Engine) sourceSlot(sourceID string) chan struct{} {
	ch, _ := e.limiter.LoadOrStore(sourceID, make(chan struct{}, 1))
	return ch.(chan struct{})
}

func (e *Engine) execute(ctx context.Context, app store.Application, in policy.Input, exec execution.Executor, runID string) error {
	e.inflight.Store(app.ID, struct{}{})
	defer e.inflight.Delete(app.ID)
	slot := e.sourceSlot(in.Source.ID)
	slot <- struct{}{}
	defer func() { <-slot }()

	contact, err := e.contact(ctx, app.SnapshotID)
	if err != nil {
		return err
	}
	cctx, cancel := context.WithTimeout(ctx, execution.SubmitTimeout)
	res, subErr := exec.Submit(cctx, execution.SubmitRequest{Application: app, Job: in.Job, Source: in.Source, Contact: contact,
		Documents: in.Documents, Answers: in.Answers, IdempotencyKey: app.IdempotencyKey})
	cancel()

	if errors.Is(subErr, execution.ErrAuth) {
		defer e.markAccountInvalid(ctx, in)
	}
	return e.Store.Tx(ctx, func(tx *sql.Tx) error {
		switch {
		case subErr == nil:
			if err := e.Store.FinishRun(ctx, tx, runID, "succeeded", "", res); err != nil {
				return err
			}
			return e.Store.Transition(ctx, tx, &app, domain.StateVerifying, store.Patch{}, map[string]any{"external_ref": res.ExternalRef})
		case errors.Is(subErr, execution.ErrRetryable):
			next := e.Store.Now().Add(backoff(app.Attempts))
			msg := subErr.Error()
			if err := e.Store.FinishRun(ctx, tx, runID, "failed", msg, nil); err != nil {
				return err
			}
			return e.Store.Transition(ctx, tx, &app, domain.StateFailedRetryable, store.Patch{LastError: &msg, NextAttemptAt: &next}, nil)
		case errors.Is(subErr, execution.ErrAuth):
			msg := subErr.Error()
			if err := e.Store.FinishRun(ctx, tx, runID, "failed", msg, nil); err != nil {
				return err
			}
			reason := "platform account needs re-authentication: " + msg
			return e.Store.Transition(ctx, tx, &app, domain.StateBlockedException, store.Patch{LastError: &msg, ExceptionReason: &reason}, nil)
		case errors.Is(subErr, execution.ErrPermanent):
			msg := subErr.Error()
			if err := e.Store.FinishRun(ctx, tx, runID, "failed", msg, nil); err != nil {
				return err
			}
			return e.Store.Transition(ctx, tx, &app, domain.StateFailedFinal, store.Patch{LastError: &msg}, nil)
		default: // ErrUncertain or anything unclassified: never assume either way
			msg := subErr.Error()
			if err := e.Store.FinishRun(ctx, tx, runID, "unknown", msg, nil); err != nil {
				return err
			}
			return e.Store.Transition(ctx, tx, &app, domain.StateSubmissionUnknown, store.Patch{LastError: &msg, ClearNextAttempt: true}, nil)
		}
	})
}

// markAccountInvalid runs after the transaction commits (it writes through the pool).
func (e *Engine) markAccountInvalid(ctx context.Context, in policy.Input) {
	if in.Account != nil {
		_ = e.Store.RecordSession(ctx, in.Account.ID, "invalid", nil, "submission rejected credentials")
	}
}

func backoff(attempt int) time.Duration {
	d := time.Duration(1<<min(attempt, 6)) * 30 * time.Second
	return min(d, 30*time.Minute)
}

// recoverExecuting handles EXECUTING found by the loop. If this process is not running the
// submission, the process that was must have died mid-call: the outcome is unknown.
func (e *Engine) recoverExecuting(ctx context.Context, app store.Application) error {
	if _, busy := e.inflight.Load(app.ID); busy {
		return nil
	}
	runs, err := e.Store.ListRuns(ctx, app.ID)
	if err != nil {
		return err
	}
	return e.Store.Tx(ctx, func(tx *sql.Tx) error {
		for _, r := range runs {
			if r.State == "started" {
				if err := e.Store.FinishRun(ctx, tx, r.ID, "unknown", "process stopped during submission", nil); err != nil {
					return err
				}
			}
		}
		msg := "process stopped during submission; reconciling"
		return e.Store.Transition(ctx, tx, &app, domain.StateSubmissionUnknown, store.Patch{LastError: &msg, ClearNextAttempt: true}, nil)
	})
}

func (e *Engine) executorFor(app store.Application) execution.Executor {
	for _, x := range e.Executors {
		if app.Executor != nil && x.Name() == *app.Executor {
			return x
		}
	}
	return nil
}

func (e *Engine) verifyStep(ctx context.Context, app store.Application) error {
	exec := e.executorFor(app)
	if exec == nil {
		reason := "verification needs the executor that submitted, but it is not available"
		return e.Store.Tx(ctx, func(tx *sql.Tx) error {
			return e.Store.Transition(ctx, tx, &app, domain.StateSubmissionUnknown, store.Patch{LastError: &reason}, nil)
		})
	}
	src, err := e.sourceOf(ctx, app)
	if err != nil {
		return err
	}
	lr, err := exec.Lookup(ctx, src, app.IdempotencyKey)
	if err != nil {
		return e.Store.Defer(ctx, app, e.Store.Now().Add(time.Minute), "verification lookup failed: "+err.Error())
	}
	return e.Store.Tx(ctx, func(tx *sql.Tx) error {
		if !lr.Found {
			msg := "ATS acknowledged the submission but the lookup cannot find it"
			return e.Store.Transition(ctx, tx, &app, domain.StateSubmissionUnknown, store.Patch{LastError: &msg}, nil)
		}
		if err := e.Store.SaveReceipt(ctx, tx, store.Receipt{ApplicationID: app.ID, Kind: "ats_api", ExternalRef: src.ID + ":" + lr.ExternalRef, Evidence: lr.Raw, Verified: true}); err != nil {
			return err
		}
		out := "submitted via " + exec.Name()
		return e.Store.Transition(ctx, tx, &app, domain.StateSubmitted, store.Patch{Submitted: true, Outcome: &out, ClearNextAttempt: true}, map[string]any{"external_ref": lr.ExternalRef})
	})
}

func (e *Engine) sourceOf(ctx context.Context, app store.Application) (store.Source, error) {
	j, err := e.Store.GetJob(ctx, app.JobID)
	if err != nil {
		return store.Source{}, err
	}
	return e.Store.GetSource(ctx, j.SourceID)
}

// reconcileStep resolves SUBMISSION_UNKNOWN: look for the application externally; if the
// lookup is authoritative and finds nothing, retrying with the SAME idempotency key is safe.
func (e *Engine) reconcileStep(ctx context.Context, app store.Application) error {
	exec := e.executorFor(app)
	if exec == nil {
		reason := "submission outcome unknown and no executor can check it; confirm on the platform"
		return e.Store.Tx(ctx, func(tx *sql.Tx) error {
			return e.Store.Transition(ctx, tx, &app, domain.StateBlockedException, store.Patch{ExceptionReason: &reason}, nil)
		})
	}
	src, err := e.sourceOf(ctx, app)
	if err != nil {
		return err
	}
	lr, err := exec.Lookup(ctx, src, app.IdempotencyKey)
	if err != nil {
		if since, perr := time.Parse(time.RFC3339Nano, app.UpdatedAt); perr == nil && e.Store.Now().Sub(since) > e.Cfg.ReconcileFor {
			reason := "could not reconcile an uncertain submission within " + e.Cfg.ReconcileFor.String() + "; check the platform"
			return e.Store.Tx(ctx, func(tx *sql.Tx) error {
				return e.Store.Transition(ctx, tx, &app, domain.StateBlockedException, store.Patch{ExceptionReason: &reason}, nil)
			})
		}
		return e.Store.Defer(ctx, app, e.Store.Now().Add(2*time.Minute), "reconcile lookup failed: "+err.Error())
	}
	return e.Store.Tx(ctx, func(tx *sql.Tx) error {
		if lr.Found {
			return e.Store.Transition(ctx, tx, &app, domain.StateVerifying, store.Patch{ClearNextAttempt: true}, map[string]any{"reconciled": "found", "external_ref": lr.ExternalRef})
		}
		if !exec.Authoritative() {
			reason := "submission outcome unknown; the platform cannot confirm absence, so it will not be retried automatically"
			return e.Store.Transition(ctx, tx, &app, domain.StateBlockedException, store.Patch{ExceptionReason: &reason}, nil)
		}
		if app.Attempts >= e.Cfg.MaxExecAttempts {
			msg := "not found after reconciliation and attempts exhausted"
			return e.Store.Transition(ctx, tx, &app, domain.StateFailedFinal, store.Patch{LastError: &msg}, nil)
		}
		return e.Store.Transition(ctx, tx, &app, domain.StatePolicyCheck, store.Patch{ClearNextAttempt: true}, map[string]any{"reconciled": "absent; safe retry with same idempotency key"})
	})
}

func (e *Engine) contact(ctx context.Context, snapshotID string) (execution.Contact, error) {
	snap, err := e.Store.GetCandidateSnapshot(ctx, snapshotID)
	if err != nil {
		return execution.Contact{}, err
	}
	var p struct {
		Profile struct {
			Data struct {
				Identity struct {
					FullName string `json:"fullName"`
				} `json:"identity"`
				Contacts []struct {
					Kind  string `json:"kind"`
					Value string `json:"value"`
				} `json:"contacts"`
			} `json:"data"`
		} `json:"profile"`
	}
	if err := json.Unmarshal(snap.Payload, &p); err != nil {
		return execution.Contact{}, err
	}
	c := execution.Contact{FullName: strings.TrimSpace(p.Profile.Data.Identity.FullName)}
	for _, ct := range p.Profile.Data.Contacts {
		switch ct.Kind {
		case "email":
			if c.Email == "" {
				c.Email = strings.TrimSpace(ct.Value)
			}
		case "phone":
			if c.Phone == "" {
				c.Phone = strings.TrimSpace(ct.Value)
			}
		}
	}
	return c, nil
}
