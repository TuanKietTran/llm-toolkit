package workflow

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/TuanKietTran/llm-toolkit/packages/job-cv-finder/core/internal/domain"
	"github.com/TuanKietTran/llm-toolkit/packages/job-cv-finder/core/internal/execution"
	"github.com/TuanKietTran/llm-toolkit/packages/job-cv-finder/core/internal/mockats"
	"github.com/TuanKietTran/llm-toolkit/packages/job-cv-finder/core/internal/secrets"
	"github.com/TuanKietTran/llm-toolkit/packages/job-cv-finder/core/internal/sources"
	"github.com/TuanKietTran/llm-toolkit/packages/job-cv-finder/core/internal/store"
)

const snapshotJSON = `{
 "schema_version": 1,
 "source": {"system": "ruxt", "exported_at": "2026-11-20T00:00:00Z"},
 "profile": {"profile_id": "profile-1", "updated_at": "2026-11-19T00:00:00Z", "data": {
   "version": 1,
   "identity": {"fullName": "Test Candidate", "headline": "Backend Engineer", "summary": "", "location": "Ho Chi Minh City"},
   "contacts": [{"kind": "email", "label": "Email", "value": "candidate@example.test"}],
   "experiences": [], "skills": [{"name": "Backend", "skills": ["Go", "PostgreSQL", "Docker"]}],
   "certifications": [], "education": [], "projects": [], "languages": ["English"]}},
 "cvs": [{"cv_id": "cv-backend", "revision": 7, "title": "Backend CV", "markdown": "# Test Candidate\n- Built Go services on PostgreSQL", "updated_at": "2026-11-18T00:00:00Z"}],
 "preferences": {"locations": ["Ho Chi Minh City"], "work_modes": ["hybrid", "remote"], "answers": {"notice_period": "One month"}}
}`

// fakeIntel stands in for the Python worker inside Go tests: it leases tasks and returns
// contract-valid results derived deterministically from the payload.
type fakeIntel struct {
	t       *testing.T
	e       *Engine
	corrupt bool // send a schema-violating result
}

func factID(v string) string {
	s := sha256.Sum256([]byte(v))
	return "f_" + hex.EncodeToString(s[:8])
}

func (f *fakeIntel) drain(ctx context.Context) int {
	n := 0
	for {
		task, err := f.e.Store.Lease(ctx, []string{TaskMatch, TaskPrepare}, "fake", time.Minute)
		if err != nil {
			f.t.Fatal(err)
		}
		if task == nil {
			return n
		}
		n++
		var p struct {
			ApplicationID string            `json:"application_id"`
			Job           domain.Job        `json:"job"`
			SnapshotID    string            `json:"snapshot_id"`
			Questions     []domain.Question `json:"questions"`
		}
		if err := json.Unmarshal(task.Payload, &p); err != nil {
			f.t.Fatal(err)
		}
		fact := map[string]any{"fact_id": factID("Go"), "category": "skill", "value": "Go", "source_document_id": "profile:profile-1",
			"source_version_id": "2026-11-19T00:00:00Z", "evidence_location": "skills[0]", "verification_status": "candidate_stated", "confidence": 1}
		var res any
		if task.Kind == TaskMatch {
			rec, elig := "match", "eligible"
			if strings.Contains(p.Job.Title, "iOS") {
				rec, elig = "no_match", "uncertain"
			}
			dim := map[string]any{"status": "met", "score": 1}
			res = map[string]any{"job_id": p.Job.ID, "snapshot_id": p.SnapshotID, "match_score": 0.8, "eligibility": elig, "confidence": 0.7,
				"dimensions":           map[string]any{"required_skills": dim, "preferred_skills": dim, "experience": dim, "seniority": dim, "location": dim, "work_mode": dim, "compensation": dim, "career_preferences": dim},
				"matched_requirements": []any{map[string]any{"requirement": "Go", "kind": "required_skill", "evidence_refs": []string{factID("Go")}}},
				"missing_requirements": []any{}, "uncertain_requirements": []any{}, "facts": []any{fact}, "recommendation": rec,
				"engine": map[string]any{"name": "fake", "version": "0"}}
			if f.corrupt {
				res = map[string]any{"job_id": p.Job.ID}
			}
		} else {
			answers := []any{}
			for _, q := range p.Questions {
				if strings.Contains(strings.ToLower(q.Label), "notice") {
					answers = append(answers, map[string]any{"question_id": q.ID, "status": "answered", "answer": "One month", "evidence_refs": []string{}, "source": "candidate_preference"})
				} else {
					answers = append(answers, map[string]any{"question_id": q.ID, "status": "unknown", "answer": nil, "evidence_refs": []string{}, "source": "none"})
				}
			}
			res = map[string]any{"application_id": p.ApplicationID,
				"selected_cv": map[string]any{"cv_id": "cv-backend", "revision": 7, "reason": "only CV", "score": 0.9},
				"documents": []any{map[string]any{"kind": "tailored_cv", "format": "markdown", "content": "# Test Candidate\n- Built Go services",
					"statements": []any{map[string]any{"text": "Built Go services", "evidence_refs": []string{factID("Go")}}}, "source_cv_id": "cv-backend", "source_cv_revision": 7}},
				"answers": answers, "validation": map[string]any{"passed": true, "issues": []any{}}, "engine": map[string]any{"name": "fake", "version": "0"}}
		}
		raw, _ := json.Marshal(res)
		_ = f.e.CompleteTask(ctx, task.ID, "fake", raw)
	}
}

type harness struct {
	t     *testing.T
	ctx   context.Context
	e     *Engine
	ats   *mockats.Server
	srv   *httptest.Server
	intel *fakeIntel
	srcID string
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	ctx := context.Background()
	st, err := store.OpenMemory(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	ats := mockats.New("acme", "Acme Payments", "candidate-key", mockats.DemoJobs())
	srv := httptest.NewServer(ats)
	t.Cleanup(srv.Close)
	key := make([]byte, 32)
	rand.Read(key)
	vault, err := secrets.New(st.DB, base64.StdEncoding.EncodeToString(key))
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range sources.Defaults() {
		st.UpsertSource(ctx, s)
	}
	src := sources.MockATS(srv.URL, "acme")
	src.Config["company"] = "Acme Payments"
	if err := st.UpsertSource(ctx, src); err != nil {
		t.Fatal(err)
	}
	ref := "account:" + src.ID
	if err := vault.Put(ctx, ref, []byte("candidate-key")); err != nil {
		t.Fatal(err)
	}
	if err := st.UpsertAccount(ctx, store.Account{ID: store.NewID("acct"), SourceID: src.ID, Label: "test", SecretRef: &ref, Status: "connected"}); err != nil {
		t.Fatal(err)
	}
	exec := &execution.AuthorizedAPIExecutor{Client: srv.Client(), Secrets: vault, Accounts: st}
	e := New(st, sources.NewRegistry(srv.Client()), map[domain.AccessMethod]execution.Executor{domain.MethodAuthorizedAPI: exec}, Config{}, nil)
	h := &harness{t: t, ctx: ctx, e: e, ats: ats, srv: srv, srcID: src.ID}
	h.intel = &fakeIntel{t: t, e: e}
	if _, err := e.IngestCandidateSnapshot(ctx, []byte(snapshotJSON)); err != nil {
		t.Fatal(err)
	}
	return h
}

// settle runs ticks and worker drains until nothing changes.
func (h *harness) settle() {
	for i := 0; i < 30; i++ {
		before := h.eventCount()
		h.intel.drain(h.ctx)
		// Waiting states re-check after WaitRecheck; make everything due for the test.
		h.e.Store.DB.Exec(`UPDATE applications SET next_attempt_at=NULL`)
		if err := h.e.Tick(h.ctx); err != nil {
			h.t.Fatal(err)
		}
		if h.eventCount() == before {
			return
		}
	}
}

func (h *harness) eventCount() int {
	var n int
	h.e.Store.DB.QueryRow(`SELECT COUNT(*) FROM execution_events`).Scan(&n)
	return n
}

func (h *harness) appFor(extID string) store.Application {
	j, err := h.e.Store.FindJobByExternal(h.ctx, h.srcID, extID)
	if err != nil {
		h.t.Fatalf("job %s: %v", extID, err)
	}
	a, err := h.e.Store.ApplicationByLogicalKey(h.ctx, j.CanonicalKey)
	if err != nil {
		h.t.Fatalf("app for %s: %v", extID, err)
	}
	return a
}

func (h *harness) discover() {
	if _, err := h.e.RunDiscovery(h.ctx, h.srcID, ""); err != nil {
		h.t.Fatal(err)
	}
}

func TestEndToEndAutomatedSubmissionWithVerification(t *testing.T) {
	h := newHarness(t)
	h.discover()
	h.settle()

	a := h.appFor("1001")
	if a.State != domain.StateSubmitted {
		t.Fatalf("1001 state %s (%v %v)", a.State, deref(a.ExceptionReason), deref(a.LastError))
	}
	if a.SelectedCVID == nil || *a.SelectedCVID != "cv-backend" || *a.SelectedCVRevision != 7 {
		t.Fatal("selected CV version not recorded")
	}
	rc, _ := h.e.Store.ListReceipts(h.ctx, a.ID)
	if len(rc) != 1 || rc[0].Kind != "ats_api" || !rc[0].Verified {
		t.Fatalf("receipts %+v", rc)
	}
	apps := h.ats.Applications()
	if len(apps) != 1 || apps[0].Fields["question_notice"] != "One month" || apps[0].Fields["email"] != "candidate@example.test" || apps[0].Files["resume"] == 0 {
		t.Fatalf("ATS received %+v", apps)
	}
	// The iOS role is not selected and never reaches the ATS.
	if s := h.appFor("1002").State; s != domain.StateNotSelected {
		t.Fatalf("1002 state %s", s)
	}
	// 1003 asks about visa sponsorship: unknown, so it must block, not guess.
	b := h.appFor("1003")
	if b.State != domain.StateBlockedException || !strings.Contains(deref(b.ExceptionReason), "visa") {
		t.Fatalf("1003 state %s reason %q", b.State, deref(b.ExceptionReason))
	}
	// Candidate answers; it proceeds and submits.
	if err := h.e.ProvideAnswer(h.ctx, b.ID, "question_visa", "No, I am a Vietnamese citizen"); err != nil {
		t.Fatal(err)
	}
	h.settle()
	if s := h.appFor("1003").State; s != domain.StateSubmitted {
		t.Fatalf("1003 after answer: %s", s)
	}
	if len(h.ats.Applications()) != 2 {
		t.Fatalf("ATS has %d applications", len(h.ats.Applications()))
	}
	// Re-discovery does not create duplicate applications or submissions.
	h.discover()
	h.settle()
	if len(h.ats.Applications()) != 2 {
		t.Fatal("duplicate submission after rediscovery")
	}
}

func TestStoredButErroredSubmissionIsReconciledNotDuplicated(t *testing.T) {
	h := newHarness(t)
	h.ats.Faults.FailAfterStore.Store(1)
	h.discover()
	h.settle()
	a := h.appFor("1001")
	if a.State != domain.StateSubmitted {
		t.Fatalf("state %s", a.State)
	}
	if n := len(h.ats.Applications()); n != 1 {
		t.Fatalf("ATS has %d applications, want exactly 1", n)
	}
	evs, _ := h.e.Store.ListEvents(h.ctx, 0, a.ID, 0)
	saw := false
	for _, ev := range evs {
		if ev.ToState != nil && *ev.ToState == string(domain.StateSubmissionUnknown) {
			saw = true
		}
	}
	if !saw {
		t.Fatal("never passed through SUBMISSION_UNKNOWN")
	}
}

func TestRejectedBeforeStoreIsRetriedSafely(t *testing.T) {
	h := newHarness(t)
	h.ats.Faults.FailBefore.Store(1)
	h.discover()
	h.settle()
	a := h.appFor("1001")
	if a.State != domain.StateSubmitted || a.Attempts != 2 {
		t.Fatalf("state %s attempts %d", a.State, a.Attempts)
	}
	if n := len(h.ats.Applications()); n != 1 {
		t.Fatalf("ATS has %d", n)
	}
}

func TestCrashDuringSubmissionRecovers(t *testing.T) {
	h := newHarness(t)
	h.discover()
	// Run until 1001 is ready for policy, then simulate a crash right after the write-ahead
	// record: the external call happened (the ATS stored it) but the process died.
	for i := 0; i < 10; i++ {
		h.intel.drain(h.ctx)
		a := h.appFor("1001")
		if a.State == domain.StatePrepared {
			break
		}
		h.e.Store.DB.Exec(`UPDATE applications SET next_attempt_at=NULL`)
		h.e.Step(h.ctx, a.ID)
	}
	a := h.appFor("1001")
	if a.State != domain.StatePrepared {
		t.Fatalf("setup: %s", a.State)
	}
	err := h.e.Store.Tx(h.ctx, func(tx *sql.Tx) error {
		if err := h.e.Store.Transition(h.ctx, tx, &a, domain.StatePolicyCheck, store.Patch{}, nil); err != nil {
			return err
		}
		name := "authorized_api"
		if err := h.e.Store.Transition(h.ctx, tx, &a, domain.StateExecuting, store.Patch{Executor: &name, IncAttempts: true}, nil); err != nil {
			return err
		}
		_, err := h.e.Store.StartRun(h.ctx, tx, a.ID, name, a.IdempotencyKey, 1)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	// The ATS did receive it before the "crash".
	in, _ := h.e.policyInput(h.ctx, a)
	exec := h.e.Executors[domain.MethodAuthorizedAPI]
	contact, _ := h.e.contact(h.ctx, a.SnapshotID)
	if _, err := exec.Submit(h.ctx, execution.SubmitRequest{Job: in.Job, Source: in.Source, Contact: contact, Documents: in.Documents, Answers: in.Answers, IdempotencyKey: a.IdempotencyKey}); err != nil {
		t.Fatal(err)
	}
	// "Restart": a fresh engine over the same store.
	h.e = New(h.e.Store, h.e.Registry, h.e.Executors, Config{}, nil)
	h.intel.e = h.e
	h.settle()
	a = h.appFor("1001")
	if a.State != domain.StateSubmitted {
		t.Fatalf("after recovery: %s", a.State)
	}
	if n := len(h.ats.Applications()); n != 1 {
		t.Fatalf("ATS has %d applications after recovery", n)
	}
	runs, _ := h.e.Store.ListRuns(h.ctx, a.ID)
	if runs[0].State != "unknown" {
		t.Fatalf("crashed run recorded as %s", runs[0].State)
	}
}

func TestContractViolationsAreDeadLetteredIntoExceptions(t *testing.T) {
	h := newHarness(t)
	h.intel.corrupt = true
	h.discover()
	for i := 0; i < 8; i++ {
		h.intel.drain(h.ctx)
		h.e.Store.DB.Exec(`UPDATE tasks SET not_before='2000-01-01T00:00:00Z'`)
		h.e.Tick(h.ctx)
	}
	a := h.appFor("1001")
	if a.State != domain.StateBlockedException || !strings.Contains(deref(a.ExceptionReason), "intel.match") {
		t.Fatalf("state %s reason %q", a.State, deref(a.ExceptionReason))
	}
	// Fix the worker and retry: the workflow resumes.
	h.intel.corrupt = false
	if err := h.e.Retry(h.ctx, a.ID); err != nil {
		t.Fatal(err)
	}
	h.settle()
	if s := h.appFor("1001").State; s != domain.StateSubmitted {
		t.Fatalf("after retry: %s", s)
	}
}

func TestLinkedInHandoffVerifiedByConfirmationEmail(t *testing.T) {
	h := newHarness(t)
	alert := sources.Email{MessageID: "<alert-1@linkedin>", From: "jobalerts-noreply@linkedin.com", Subject: "Acme is hiring", Date: time.Now(),
		Text: "Backend Engineer (Go)\nAcme Fintech\nHo Chi Minh City, Vietnam\nView job: https://www.linkedin.com/comm/jobs/view/4486000001/?x=1\n"}
	r, err := h.e.IntakeEmail(h.ctx, alert)
	if err != nil || r.Jobs == nil || r.Jobs.New != 1 || r.Jobs.Applications != 1 {
		t.Fatalf("%+v %v", r, err)
	}
	h.settle()
	j, _ := h.e.Store.FindJobByExternal(h.ctx, "linkedin", "4486000001")
	a, _ := h.e.Store.ApplicationByLogicalKey(h.ctx, j.CanonicalKey)
	if a.State != domain.StateHandoffPending {
		t.Fatalf("state %s (%s)", a.State, deref(a.ExceptionReason))
	}
	pol, _ := h.e.Store.ListPolicyDecisions(h.ctx, a.ID)
	if pol[0]["outcome"] != domain.PolicyHandoff {
		t.Fatalf("policy %+v", pol)
	}
	confirm := sources.Email{MessageID: "<confirm-1@linkedin>", From: "jobs-noreply@linkedin.com", Subject: "Candidate, your application was sent to Acme Fintech", Date: time.Now(),
		Text: "Your application was sent to Acme Fintech\n\nBackend Engineer (Go)\nAcme Fintech\nHo Chi Minh City, Vietnam\nView job: https://www.linkedin.com/comm/jobs/view/4486000001/?t=1\n\n----------------------------------------------\nApplied on November 20, 2026"}
	cr, err := h.e.IntakeEmail(h.ctx, confirm)
	if err != nil || !cr.Verified || cr.ApplicationID != a.ID {
		t.Fatalf("%+v %v", cr, err)
	}
	a, _ = h.e.Store.GetApplication(h.ctx, a.ID)
	if a.State != domain.StateSubmitted {
		t.Fatalf("after confirmation: %s", a.State)
	}
	if again, _ := h.e.IntakeEmail(h.ctx, confirm); !again.Duplicate {
		t.Fatal("replayed email processed twice")
	}
}

func TestConfirmationForUnknownJobRecordsExternalApplication(t *testing.T) {
	h := newHarness(t)
	c := sources.Email{MessageID: "<c2@itviec>", From: "itviec@itviec.com", Date: time.Now(),
		Subject: "Chúc mừng! Hồ sơ của bạn cho vị trí Data Engineer tại Example Data đã được gửi đi!"}
	r, err := h.e.IntakeEmail(h.ctx, c)
	if err != nil || !r.Verified {
		t.Fatalf("%+v %v", r, err)
	}
	a, _ := h.e.Store.GetApplication(h.ctx, r.ApplicationID)
	if a.State != domain.StateSubmitted {
		t.Fatalf("state %s", a.State)
	}
	// Later the same job shows up in an ITviec alert: no second application.
	alert := sources.Email{MessageID: "<a3@itviec>", From: "itviec@itviec.com", Subject: "jobs", Date: time.Now(),
		Text: "Job 1: Data Engineer\nEmployer: Example Data\nSalary: 1,000 - 2,000 USD\nRequired Skills: Python\n"}
	ar, _ := h.e.IntakeEmail(h.ctx, alert)
	if ar.Jobs.Applications != 0 {
		t.Fatalf("re-applied to an already-submitted job: %+v", ar.Jobs)
	}
}

func TestInvalidSnapshotRejected(t *testing.T) {
	h := newHarness(t)
	_, err := h.e.IngestCandidateSnapshot(h.ctx, []byte(`{"schema_version":1}`))
	if err == nil {
		t.Fatal("invalid snapshot accepted")
	}
	if _, ok := err.(*ValidationError); !ok {
		t.Fatalf("want ValidationError, got %T", err)
	}
}

func deref(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

var _ = fmt.Sprint
