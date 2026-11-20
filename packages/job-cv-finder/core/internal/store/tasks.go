package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"time"
)

// Task is a durable unit of work for a Python intelligence worker. The queue lives in the
// same database as workflow state, so enqueueing commits atomically with the transition
// that requested it (no dual-write problem, no extra infrastructure).
type Task struct {
	ID             string          `json:"id"`
	Kind           string          `json:"kind"`
	ApplicationID  *string         `json:"application_id"`
	IdempotencyKey string          `json:"idempotency_key"`
	Payload        json.RawMessage `json:"payload"`
	State          string          `json:"state"`
	Attempts       int             `json:"attempts"`
	MaxAttempts    int             `json:"max_attempts"`
	LeaseOwner     *string         `json:"lease_owner"`
	LeaseUntil     *string         `json:"lease_until"`
	Result         json.RawMessage `json:"result,omitempty"`
	Error          *string         `json:"error"`
}

// Task states.
const (
	TaskQueued = "queued"
	TaskLeased = "leased"
	TaskDone   = "done"
	TaskDead   = "dead"
)

// ErrLeaseLost means the worker no longer holds the task (expired and re-leased, or done).
var ErrLeaseLost = errors.New("task lease lost")

// Enqueue adds a task inside tx. Re-enqueueing the same idempotency key is a no-op.
func (s *Store) Enqueue(ctx context.Context, tx *sql.Tx, kind, appID, idem string, payload any, maxAttempts int) (string, error) {
	if maxAttempts <= 0 {
		maxAttempts = 5
	}
	var id string
	err := tx.QueryRowContext(ctx, `SELECT id FROM tasks WHERE idempotency_key=?`, idem).Scan(&id)
	if err == nil {
		return id, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return "", err
	}
	id = NewID("task")
	now := ts(s.Now())
	var app any
	if appID != "" {
		app = appID
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO tasks(id, kind, application_id, idempotency_key, payload, state, max_attempts, not_before, created_at, updated_at) VALUES(?,?,?,?,?,?,?,?,?,?)`,
		id, kind, app, idem, js(payload), TaskQueued, maxAttempts, now, now, now)
	if err != nil {
		return "", err
	}
	return id, s.Event(ctx, tx, appID, "task.enqueued", "", "", map[string]any{"task_id": id, "kind": kind})
}

// Lease hands the oldest ready task of the given kinds to owner for d. Expired leases are
// reclaimed first, which is how a crashed worker's task gets redelivered.
func (s *Store) Lease(ctx context.Context, kinds []string, owner string, d time.Duration) (*Task, error) {
	var t *Task
	err := s.Tx(ctx, func(tx *sql.Tx) error {
		if err := s.reclaim(ctx, tx); err != nil {
			return err
		}
		now := s.Now()
		q := `SELECT id FROM tasks WHERE state=? AND not_before<=? AND kind IN (?` + strings.Repeat(",?", len(kinds)-1) + `) ORDER BY created_at LIMIT 1`
		args := []any{TaskQueued, ts(now)}
		for _, k := range kinds {
			args = append(args, k)
		}
		var id string
		if err := tx.QueryRowContext(ctx, q, args...).Scan(&id); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return nil
			}
			return err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE tasks SET state=?, lease_owner=?, lease_until=?, attempts=attempts+1, updated_at=? WHERE id=? AND state=?`,
			TaskLeased, owner, ts(now.Add(d)), ts(now), id, TaskQueued); err != nil {
			return err
		}
		got, err := getTask(ctx, tx, id)
		if err != nil {
			return err
		}
		t = &got
		return s.Event(ctx, tx, deref(got.ApplicationID), "task.leased", "", "", map[string]any{"task_id": id, "kind": got.Kind, "worker": owner, "attempt": got.Attempts})
	})
	return t, err
}

// reclaim returns expired leases to the queue, or to dead once attempts are exhausted.
func (s *Store) reclaim(ctx context.Context, tx *sql.Tx) error {
	now := ts(s.Now())
	rows, err := tx.QueryContext(ctx, `SELECT id, attempts, max_attempts, application_id FROM tasks WHERE state=? AND lease_until<?`, TaskLeased, now)
	if err != nil {
		return err
	}
	type exp struct {
		id       string
		att, max int
		app      sql.NullString
	}
	var expired []exp
	for rows.Next() {
		var e exp
		if err := rows.Scan(&e.id, &e.att, &e.max, &e.app); err != nil {
			rows.Close()
			return err
		}
		expired = append(expired, e)
	}
	rows.Close()
	for _, e := range expired {
		state := TaskQueued
		if e.att >= e.max {
			state = TaskDead
		}
		if _, err := tx.ExecContext(ctx, `UPDATE tasks SET state=?, lease_owner=NULL, lease_until=NULL, error=COALESCE(error,'lease expired'), updated_at=? WHERE id=?`, state, now, e.id); err != nil {
			return err
		}
		if err := s.Event(ctx, tx, e.app.String, "task.lease_expired", "", "", map[string]any{"task_id": e.id, "now": state}); err != nil {
			return err
		}
	}
	return nil
}

// ReclaimExpired is the scheduler's periodic sweep.
func (s *Store) ReclaimExpired(ctx context.Context) error {
	return s.Tx(ctx, func(tx *sql.Tx) error { return s.reclaim(ctx, tx) })
}

// CompleteTask stores a result if owner still holds the lease. The caller (workflow) must
// already have validated result against its contract; apply runs in the same transaction so
// the task is only marked done if the workflow change commits.
func (s *Store) CompleteTask(ctx context.Context, id, owner string, result []byte, apply func(tx *sql.Tx, t Task) error) error {
	return s.Tx(ctx, func(tx *sql.Tx) error {
		t, err := getTask(ctx, tx, id)
		if err != nil {
			return err
		}
		if t.State != TaskLeased || deref(t.LeaseOwner) != owner {
			return ErrLeaseLost
		}
		if apply != nil {
			if err := apply(tx, t); err != nil {
				return err
			}
		}
		_, err = tx.ExecContext(ctx, `UPDATE tasks SET state=?, result=?, lease_owner=NULL, lease_until=NULL, error=NULL, updated_at=? WHERE id=?`, TaskDone, string(result), ts(s.Now()), id)
		if err != nil {
			return err
		}
		return s.Event(ctx, tx, deref(t.ApplicationID), "task.completed", "", "", map[string]any{"task_id": id, "kind": t.Kind})
	})
}

// FailTask records a failure; the task is retried with backoff or marked dead. onDead runs
// in the same transaction when the task dies, so the workflow can surface an exception.
func (s *Store) FailTask(ctx context.Context, id, owner, msg string, retryable bool, onDead func(tx *sql.Tx, t Task) error) (string, error) {
	var final string
	err := s.Tx(ctx, func(tx *sql.Tx) error {
		t, err := getTask(ctx, tx, id)
		if err != nil {
			return err
		}
		if t.State != TaskLeased || deref(t.LeaseOwner) != owner {
			return ErrLeaseLost
		}
		final = TaskQueued
		if !retryable || t.Attempts >= t.MaxAttempts {
			final = TaskDead
		}
		backoff := time.Duration(1<<min(t.Attempts, 8)) * time.Second
		if _, err := tx.ExecContext(ctx, `UPDATE tasks SET state=?, error=?, lease_owner=NULL, lease_until=NULL, not_before=?, updated_at=? WHERE id=?`,
			final, msg, ts(s.Now().Add(backoff)), ts(s.Now()), id); err != nil {
			return err
		}
		if err := s.Event(ctx, tx, deref(t.ApplicationID), "task.failed", "", "", map[string]any{"task_id": id, "error": msg, "now": final}); err != nil {
			return err
		}
		if final == TaskDead && onDead != nil {
			return onDead(tx, t)
		}
		return nil
	})
	return final, err
}

// DeadTasksWithoutHandling lists dead tasks whose application is still waiting on them
// (e.g. a lease expired for the last time). The workflow turns these into exceptions.
func (s *Store) DeadTasks(ctx context.Context, kind string) ([]Task, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT `+taskCols+` FROM tasks WHERE state=? AND kind=?`, TaskDead, kind)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Task
	for rows.Next() {
		t, err := scanTask(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// ListTasks returns recent tasks for observability.
func (s *Store) ListTasks(ctx context.Context, limit int) ([]Task, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT `+taskCols+` FROM tasks ORDER BY updated_at DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Task
	for rows.Next() {
		t, err := scanTask(rows)
		if err != nil {
			return nil, err
		}
		t.Payload = nil // payloads hold candidate data; not for listings
		out = append(out, t)
	}
	return out, rows.Err()
}

const taskCols = `id, kind, application_id, idempotency_key, payload, state, attempts, max_attempts, lease_owner, lease_until, result, error`

func scanTask(row interface{ Scan(...any) error }) (Task, error) {
	var t Task
	var app, owner, until, res, e sql.NullString
	var payload string
	err := row.Scan(&t.ID, &t.Kind, &app, &t.IdempotencyKey, &payload, &t.State, &t.Attempts, &t.MaxAttempts, &owner, &until, &res, &e)
	if errors.Is(err, sql.ErrNoRows) {
		return t, ErrNotFound
	}
	if err != nil {
		return t, err
	}
	t.ApplicationID, t.LeaseOwner, t.LeaseUntil, t.Error = nullStr(app), nullStr(owner), nullStr(until), nullStr(e)
	t.Payload = json.RawMessage(payload)
	if res.Valid {
		t.Result = json.RawMessage(res.String)
	}
	return t, nil
}

func getTask(ctx context.Context, q querier, id string) (Task, error) {
	return scanTask(q.QueryRowContext(ctx, `SELECT `+taskCols+` FROM tasks WHERE id=?`, id))
}

// GetTask loads one task.
func (s *Store) GetTask(ctx context.Context, id string) (Task, error) { return getTask(ctx, s.DB, id) }

func deref(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

// InboundMessage is a processed email (job alert or application confirmation).
type InboundMessage struct {
	ID             string          `json:"id"`
	MessageID      string          `json:"message_id"`
	Sender         string          `json:"sender"`
	Subject        string          `json:"subject"`
	ReceivedAt     string          `json:"received_at"`
	Classification string          `json:"classification"`
	Parsed         json.RawMessage `json:"parsed"`
}

// RecordMessage stores a message once; ok=false means it was already processed.
func (s *Store) RecordMessage(ctx context.Context, m InboundMessage) (bool, error) {
	res, err := s.DB.ExecContext(ctx, `INSERT INTO inbound_messages(id, message_id, sender, subject, received_at, classification, parsed, processed_at) VALUES(?,?,?,?,?,?,?,?)
		ON CONFLICT(message_id) DO NOTHING`, NewID("msg"), m.MessageID, m.Sender, m.Subject, m.ReceivedAt, m.Classification, string(m.Parsed), ts(s.Now()))
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n == 1, nil
}
