package store

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	"github.com/TuanKietTran/llm-toolkit/packages/job-cv-finder/core/internal/domain"
	"github.com/TuanKietTran/llm-toolkit/packages/job-cv-finder/core/internal/normalize"
)

type clock struct{ t time.Time }

func (c *clock) now() time.Time { return c.t }

func newStore(t *testing.T) (*Store, *clock) {
	t.Helper()
	s, err := OpenMemory(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	c := &clock{t: time.Date(2026, 11, 20, 8, 0, 0, 0, time.UTC)}
	s.Now = c.now
	for _, id := range []string{"linkedin", "itviec"} {
		if err := s.UpsertSource(context.Background(), Source{ID: id, Kind: id + "_email", Name: id, Enabled: true}); err != nil {
			t.Fatal(err)
		}
	}
	return s, c
}

func TestMigrationsAreIdempotent(t *testing.T) {
	s, _ := newStore(t)
	if err := s.migrate(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestIngestDedupAcrossSourcesAndChangeDetection(t *testing.T) {
	ctx := context.Background()
	s, c := newStore(t)
	now := c.t
	a := normalize.Normalize("linkedin", domain.RawJob{ExternalJobID: "4486004372", Company: "giftee Tech Vietnam", Title: "Software Engineer (Middle to Senior)", Partial: true}, now)
	ja, out, err := s.IngestJob(ctx, a, "text/plain", []byte("alert-1"))
	if err != nil || out != IngestNew {
		t.Fatalf("first ingest: %v %v", out, err)
	}
	// Same email replayed: seen, no new row.
	if _, out, _ := s.IngestJob(ctx, a, "text/plain", []byte("alert-1")); out != IngestSeen {
		t.Fatalf("replay: %v", out)
	}
	// Same real job on ITviec: duplicate pointing at the LinkedIn primary.
	b := normalize.Normalize("itviec", domain.RawJob{ExternalJobID: "itv-1", Company: "Giftee Tech", Title: "Software Engineer - Middle to Senior", Description: "Go and Docker"}, now)
	jb, out, err := s.IngestJob(ctx, b, "text/plain", []byte("alert-2"))
	if err != nil || out != IngestDuplicate || jb.DuplicateOf != ja.ID {
		t.Fatalf("dup: %v %v %+v", out, err, jb.DuplicateOf)
	}
	// Full description arrives for the LinkedIn job: changed, snapshot preserved.
	a2 := normalize.Normalize("linkedin", domain.RawJob{ExternalJobID: "4486004372", Company: "giftee Tech Vietnam", Title: "Software Engineer (Middle to Senior)", Description: "We use Go"}, now)
	j2, out, err := s.IngestJob(ctx, a2, "text/plain", []byte("full"))
	if err != nil || out != IngestChanged || j2.ID != ja.ID || j2.Status != "changed" {
		t.Fatalf("change: %v %v %+v", out, err, j2)
	}
	// A later partial alert must not clobber the full description.
	if got, out, _ := s.IngestJob(ctx, a, "text/plain", []byte("alert-3")); out != IngestSeen || got.Description != "We use Go" {
		t.Fatalf("partial clobbered full: %v %q", out, got.Description)
	}
	var snaps int
	s.DB.QueryRow(`SELECT COUNT(*) FROM job_snapshots`).Scan(&snaps)
	if snaps != 4 {
		t.Fatalf("snapshots = %d, want 4 (history kept)", snaps)
	}
}

func seedApp(t *testing.T, s *Store) (Application, domain.Job) {
	t.Helper()
	ctx := context.Background()
	j, _, err := s.IngestJob(ctx, normalize.Normalize("linkedin", domain.RawJob{ExternalJobID: "1", Company: "Acme", Title: "Go Engineer"}, s.Now()), "", nil)
	if err != nil {
		t.Fatal(err)
	}
	snap, err := s.SaveCandidateSnapshot(ctx, "p1", "2026-11-01T00:00:00Z", []CVRef{{CVID: "cv1", Revision: 3}}, []byte(`{"x":1}`))
	if err != nil {
		t.Fatal(err)
	}
	app, created, err := s.CreateApplication(ctx, j, snap.ID, nil)
	if err != nil || !created {
		t.Fatal(err)
	}
	return app, j
}

func TestCreateApplicationIsIdempotentPerRealJob(t *testing.T) {
	s, _ := newStore(t)
	app, j := seedApp(t, s)
	again, created, err := s.CreateApplication(context.Background(), j, app.SnapshotID, nil)
	if err != nil || created || again.ID != app.ID {
		t.Fatalf("duplicate application created: %v %v", created, err)
	}
}

func TestTransitionCASAndLegality(t *testing.T) {
	ctx := context.Background()
	s, _ := newStore(t)
	app, _ := seedApp(t, s)
	stale := app
	err := s.Tx(ctx, func(tx *sql.Tx) error { return s.Transition(ctx, tx, &app, domain.StateMatched, Patch{}, nil) })
	if err != nil || app.State != domain.StateMatched || app.Version != 2 {
		t.Fatalf("transition: %v %+v", err, app)
	}
	err = s.Tx(ctx, func(tx *sql.Tx) error { return s.Transition(ctx, tx, &stale, domain.StateMatched, Patch{}, nil) })
	if !errors.Is(err, ErrConflict) {
		t.Fatalf("stale CAS should conflict, got %v", err)
	}
	err = s.Tx(ctx, func(tx *sql.Tx) error { return s.Transition(ctx, tx, &app, domain.StateSubmitted, Patch{}, nil) })
	if err == nil {
		t.Fatal("MATCHED -> SUBMITTED must be illegal")
	}
	evs, _ := s.ListEvents(ctx, 0, app.ID, 0)
	if len(evs) != 2 || evs[1].Kind != "state.changed" {
		t.Fatalf("events: %+v", evs)
	}
}

func TestTaskLeaseExpiryRedeliveryAndDeadLetter(t *testing.T) {
	ctx := context.Background()
	s, c := newStore(t)
	app, _ := seedApp(t, s)
	var id string
	s.Tx(ctx, func(tx *sql.Tx) error {
		var err error
		id, err = s.Enqueue(ctx, tx, "intel.match", app.ID, "k1", map[string]any{"a": 1}, 2)
		if err != nil {
			return err
		}
		id2, _ := s.Enqueue(ctx, tx, "intel.match", app.ID, "k1", map[string]any{"a": 1}, 2)
		if id2 != id {
			t.Errorf("enqueue not idempotent")
		}
		return nil
	})
	t1, err := s.Lease(ctx, []string{"intel.match"}, "w1", time.Minute)
	if err != nil || t1 == nil || t1.ID != id {
		t.Fatalf("lease: %v %+v", err, t1)
	}
	if t2, _ := s.Lease(ctx, []string{"intel.match"}, "w2", time.Minute); t2 != nil {
		t.Fatal("leased task delivered twice")
	}
	// Worker 1 crashes; lease expires; worker 2 gets it.
	c.t = c.t.Add(2 * time.Minute)
	t2, _ := s.Lease(ctx, []string{"intel.match"}, "w2", time.Minute)
	if t2 == nil || t2.ID != id || t2.Attempts != 2 {
		t.Fatalf("redelivery: %+v", t2)
	}
	// Worker 1 wakes up late: its completion must be rejected.
	if err := s.CompleteTask(ctx, id, "w1", []byte(`{}`), nil); !errors.Is(err, ErrLeaseLost) {
		t.Fatalf("stale completion accepted: %v", err)
	}
	// Worker 2 fails on the last attempt -> dead, onDead hook runs.
	called := false
	final, err := s.FailTask(ctx, id, "w2", "boom", true, func(tx *sql.Tx, _ Task) error { called = true; return nil })
	if err != nil || final != TaskDead || !called {
		t.Fatalf("dead letter: %v %v %v", final, err, called)
	}
}

func countReceipts(t *testing.T, s *Store, appID string) int {
	t.Helper()
	rc, err := s.ListReceipts(context.Background(), appID)
	if err != nil {
		t.Fatal(err)
	}
	return len(rc)
}

// Regression (2026-11-22, live run): UNIQUE(kind, external_ref) with DO NOTHING silently dropped the
// receipt of a second application whose platform reference had been seen before, while the
// application still became SUBMITTED.
func TestReceiptReferenceReuseKeepsEachApplicationsReceipt(t *testing.T) {
	ctx := context.Background()
	s, _ := newStore(t)
	first, _ := seedApp(t, s)
	j2, _, err := s.IngestJob(ctx, normalize.Normalize("linkedin", domain.RawJob{ExternalJobID: "2", Company: "Beta", Title: "Platform Engineer"}, s.Now()), "", nil)
	if err != nil {
		t.Fatal(err)
	}
	second, created, err := s.CreateApplication(ctx, j2, first.SnapshotID, nil)
	if err != nil || !created {
		t.Fatal(err)
	}
	save := func(app Application) {
		t.Helper()
		if err := s.Tx(ctx, func(tx *sql.Tx) error {
			return s.SaveReceipt(ctx, tx, Receipt{ApplicationID: app.ID, Kind: "ats_api", ExternalRef: "mockats:demo:mock-app-1", Evidence: []byte(`{}`), Verified: true})
		}); err != nil {
			t.Fatal(err)
		}
	}
	save(first)
	save(second) // same platform reference, different application
	if countReceipts(t, s, first.ID) != 1 || countReceipts(t, s, second.ID) != 1 {
		t.Fatalf("receipts: first=%d second=%d", countReceipts(t, s, first.ID), countReceipts(t, s, second.ID))
	}
	save(first) // replaying the same receipt stays a no-op
	save(second)
	if countReceipts(t, s, first.ID) != 1 || countReceipts(t, s, second.ID) != 1 {
		t.Fatal("replay created duplicate receipts")
	}
}

func TestSubmittedIsRefusedWithoutAReceipt(t *testing.T) {
	ctx := context.Background()
	s, _ := newStore(t)
	app, _ := seedApp(t, s)
	if err := s.Tx(ctx, func(tx *sql.Tx) error { return s.Transition(ctx, tx, &app, domain.StateVerifying, Patch{}, nil) }); err != nil {
		t.Fatal(err)
	}
	err := s.Tx(ctx, func(tx *sql.Tx) error {
		return s.Transition(ctx, tx, &app, domain.StateSubmitted, Patch{Submitted: true}, nil)
	})
	if err == nil {
		t.Fatal("SUBMITTED accepted without a receipt")
	}
	if got, _ := s.GetApplication(ctx, app.ID); got.State != domain.StateVerifying {
		t.Fatalf("state changed despite the refusal: %s", got.State)
	}
	if err := s.Tx(ctx, func(tx *sql.Tx) error {
		if err := s.SaveReceipt(ctx, tx, Receipt{ApplicationID: app.ID, Kind: "user_attested", ExternalRef: app.ID + ":a", Evidence: []byte(`{}`)}); err != nil {
			return err
		}
		return s.Transition(ctx, tx, &app, domain.StateSubmitted, Patch{Submitted: true}, nil)
	}); err != nil {
		t.Fatalf("SUBMITTED with a receipt: %v", err)
	}
}
