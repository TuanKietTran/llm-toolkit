package workflow

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/TuanKietTran/llm-toolkit/packages/job-cv-finder/core/internal/domain"
	"github.com/TuanKietTran/llm-toolkit/packages/job-cv-finder/core/internal/store"
)

// Candidate actions. These are the only ways a human changes workflow state, and each is
// recorded as an event with the actor "candidate".

// OverrideSelect moves a NOT_SELECTED application forward on the candidate's decision.
func (e *Engine) OverrideSelect(ctx context.Context, appID, note string) error {
	return e.act(ctx, appID, func(tx *sql.Tx, app *store.Application) error {
		if app.State != domain.StateNotSelected {
			return &ValidationError{Err: fmt.Errorf("only NOT_SELECTED applications can be overridden (is %s)", app.State)}
		}
		n := "override:select " + note
		return e.Store.Transition(ctx, tx, app, domain.StateSelected, store.Patch{Notes: &n}, actor())
	})
}

// Rematch re-runs matching (after the candidate enriches the job or sends new Ruxt data).
func (e *Engine) Rematch(ctx context.Context, appID string) error {
	return e.rematch(ctx, appID, actor())
}

func (e *Engine) rematch(ctx context.Context, appID string, by map[string]any) error {
	return e.act(ctx, appID, func(tx *sql.Tx, app *store.Application) error {
		snap, err := e.Store.LatestCandidateSnapshot(ctx)
		if err != nil {
			return &ValidationError{Err: errors.New("no candidate snapshot from Ruxt yet")}
		}
		if snap.ID != app.SnapshotID {
			if _, err := tx.ExecContext(ctx, `UPDATE applications SET snapshot_id=? WHERE id=? AND version=?`, snap.ID, app.ID, app.Version); err != nil {
				return err
			}
			app.SnapshotID = snap.ID
		}
		if err := e.Store.Transition(ctx, tx, app, domain.StateMatching, store.Patch{ClearException: true, ClearNextAttempt: true}, by); err != nil {
			return err
		}
		j, err := e.Store.GetJob(ctx, app.JobID)
		if err != nil {
			return err
		}
		return e.enqueueMatch(ctx, tx, *app, j)
	})
}

// Retry resumes a BLOCKED_EXCEPTION application from the furthest completed step.
func (e *Engine) Retry(ctx context.Context, appID string) error {
	return e.act(ctx, appID, func(tx *sql.Tx, app *store.Application) error {
		if app.State != domain.StateBlockedException {
			return &ValidationError{Err: fmt.Errorf("only BLOCKED_EXCEPTION applications can be retried (is %s)", app.State)}
		}
		docs, err := e.Store.ListDocuments(ctx, app.ID)
		if err != nil {
			return err
		}
		switch {
		case app.MatchID == nil:
			if err := e.Store.Transition(ctx, tx, app, domain.StateMatching, store.Patch{ClearException: true, ClearNextAttempt: true}, actor()); err != nil {
				return err
			}
			j, err := e.Store.GetJob(ctx, app.JobID)
			if err != nil {
				return err
			}
			return e.enqueueMatch(ctx, tx, *app, j)
		case len(docs) == 0:
			return e.Store.Transition(ctx, tx, app, domain.StateSelected, store.Patch{ClearException: true, ClearNextAttempt: true}, actor())
		default:
			return e.Store.Transition(ctx, tx, app, domain.StatePolicyCheck, store.Patch{ClearException: true, ClearNextAttempt: true}, actor())
		}
	})
}

// Regenerate discards prepared documents' eligibility by preparing again.
func (e *Engine) Regenerate(ctx context.Context, appID string) error {
	return e.act(ctx, appID, func(tx *sql.Tx, app *store.Application) error {
		if app.State != domain.StateBlockedException {
			return &ValidationError{Err: fmt.Errorf("regenerate is available from BLOCKED_EXCEPTION (is %s)", app.State)}
		}
		if app.MatchID == nil {
			return &ValidationError{Err: errors.New("no match yet; use retry")}
		}
		if err := e.Store.Transition(ctx, tx, app, domain.StatePreparing, store.Patch{ClearException: true, ClearNextAttempt: true}, actor()); err != nil {
			return err
		}
		return e.enqueuePrepare(ctx, tx, *app)
	})
}

// ProvideAnswer stores a candidate-written answer for a question the system could not
// answer from evidence. If nothing else is missing, the application returns to policy.
func (e *Engine) ProvideAnswer(ctx context.Context, appID, questionID, answer string) error {
	answer = strings.TrimSpace(answer)
	if answer == "" {
		return &ValidationError{Err: errors.New("answer is empty")}
	}
	return e.act(ctx, appID, func(tx *sql.Tx, app *store.Application) error {
		answers, err := e.Store.ListAnswers(ctx, app.ID)
		if err != nil {
			return err
		}
		found := false
		stillMissing := false
		for i := range answers {
			if answers[i].QuestionID == questionID {
				found = true
				answers[i].Status, answers[i].Answer, answers[i].Source, answers[i].EvidenceRefs = "answered", &answer, "candidate_provided", []string{}
			}
			if answers[i].Required && answers[i].Status != "answered" {
				stillMissing = true
			}
		}
		if !found {
			return &ValidationError{Err: fmt.Errorf("question %s is not part of this application", questionID)}
		}
		if err := e.Store.SaveAnswers(ctx, tx, app.ID, answers); err != nil {
			return err
		}
		if err := e.Store.Event(ctx, tx, app.ID, "answer.provided", "", "", map[string]any{"question_id": questionID, "actor": "candidate"}); err != nil {
			return err
		}
		if app.State == domain.StateBlockedException && !stillMissing {
			return e.Store.Transition(ctx, tx, app, domain.StatePolicyCheck, store.Patch{ClearException: true, ClearNextAttempt: true}, actor())
		}
		return nil
	})
}

// AttestSubmitted records that the candidate submitted on the platform themselves. The
// receipt is kept as unverified evidence until a platform confirmation email arrives.
func (e *Engine) AttestSubmitted(ctx context.Context, appID, evidence, evidenceURL string) error {
	evidence = strings.TrimSpace(evidence)
	if len(evidence) < 10 {
		return &ValidationError{Err: errors.New("describe the evidence (e.g. confirmation number, page text, date) in at least 10 characters")}
	}
	return e.act(ctx, appID, func(tx *sql.Tx, app *store.Application) error {
		if app.State != domain.StateHandoffPending && app.State != domain.StateBlockedException {
			return &ValidationError{Err: fmt.Errorf("attestation applies to HANDOFF_PENDING or BLOCKED_EXCEPTION (is %s)", app.State)}
		}
		if err := e.Store.SaveReceipt(ctx, tx, store.Receipt{ApplicationID: app.ID, Kind: "user_attested", ExternalRef: app.ID + ":" + store.NewID("att"),
			Evidence: mustJSON(map[string]string{"evidence": evidence, "url": evidenceURL}), Verified: false}); err != nil {
			return err
		}
		if err := e.Store.Transition(ctx, tx, app, domain.StateVerifying, store.Patch{}, actor()); err != nil {
			return err
		}
		out := "submitted by the candidate (attested; awaiting platform confirmation email)"
		return e.Store.Transition(ctx, tx, app, domain.StateSubmitted, store.Patch{Submitted: true, Outcome: &out, ClearException: true}, map[string]any{"receipt": "user_attested", "actor": "candidate"})
	})
}

// RecordOutcome stores a later outcome (interview, rejection, offer) as notes + event.
func (e *Engine) RecordOutcome(ctx context.Context, appID, outcome string) error {
	outcome = strings.TrimSpace(outcome)
	if outcome == "" {
		return &ValidationError{Err: errors.New("outcome is empty")}
	}
	return e.Store.Tx(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `UPDATE applications SET outcome=?, updated_at=? WHERE id=?`, outcome, e.Store.Now().Format("2006-01-02T15:04:05.999999999Z07:00"), appID)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return store.ErrNotFound
		}
		return e.Store.Event(ctx, tx, appID, "outcome.recorded", "", "", map[string]any{"outcome": outcome, "actor": "candidate"})
	})
}

func (e *Engine) act(ctx context.Context, appID string, fn func(tx *sql.Tx, app *store.Application) error) error {
	err := e.Store.Tx(ctx, func(tx *sql.Tx) error {
		app, err := e.Store.GetApplicationTx(ctx, tx, appID)
		if err != nil {
			return err
		}
		return fn(tx, &app)
	})
	if err == nil {
		e.Wake()
	}
	return err
}

func actor() map[string]any { return map[string]any{"actor": "candidate"} }
