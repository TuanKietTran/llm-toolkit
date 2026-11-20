// Package store is the durable persistence layer (SQLite via modernc, pure Go). It is the
// single owner of workflow state: every state change goes through a compare-and-swap on
// applications.version and writes an execution_event in the same transaction.
package store

import (
	"context"
	"crypto/rand"
	"database/sql"
	"embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

//go:embed migrations/*.sql
var migrationFS embed.FS

// ErrNotFound is returned when a row does not exist.
var ErrNotFound = errors.New("not found")

// ErrConflict is returned when a compare-and-swap loses a race.
var ErrConflict = errors.New("conflict: state changed concurrently")

// Store wraps the database. All methods are safe for concurrent use.
type Store struct {
	DB      *sql.DB
	Now     func() time.Time
	cleanup func()
}

// Open opens (creating if needed) the database at path and applies migrations.
// Use ":memory:" only through OpenMemory, since each pooled connection would get its own db.
func Open(ctx context.Context, path string) (*Store, error) {
	dsn := fmt.Sprintf("file:%s?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)&_pragma=synchronous(NORMAL)&_txlock=immediate", path)
	return open(ctx, dsn)
}

// OpenMemory opens a private throwaway database for tests. It is file-backed (in a temp
// dir removed on Close) because WAL concurrency, unlike shared-cache memory, lets reads run
// while a write transaction is open, exactly as in production.
func OpenMemory(ctx context.Context) (*Store, error) {
	dir, err := os.MkdirTemp("", "jcf-test-")
	if err != nil {
		return nil, err
	}
	s, err := Open(ctx, filepath.Join(dir, "test.db"))
	if err != nil {
		os.RemoveAll(dir)
		return nil, err
	}
	s.cleanup = func() { os.RemoveAll(dir) }
	return s, nil
}

func open(ctx context.Context, dsn string) (*Store, error) {
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	// WAL: many readers alongside one writer; concurrent writers wait on busy_timeout.
	db.SetMaxOpenConns(8)
	s := &Store{DB: db, Now: func() time.Time { return time.Now().UTC() }}
	if err := s.migrate(ctx); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}

// Close closes the database.
func (s *Store) Close() error {
	err := s.DB.Close()
	if s.cleanup != nil {
		s.cleanup()
	}
	return err
}

func (s *Store) migrate(ctx context.Context) error {
	if _, err := s.DB.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (name TEXT PRIMARY KEY, applied_at TEXT NOT NULL)`); err != nil {
		return err
	}
	names, err := fs.Glob(migrationFS, "migrations/*.sql")
	if err != nil {
		return err
	}
	sort.Strings(names)
	for _, n := range names {
		var exists int
		if err := s.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM schema_migrations WHERE name=?`, n).Scan(&exists); err != nil {
			return err
		}
		if exists > 0 {
			continue
		}
		body, err := migrationFS.ReadFile(n)
		if err != nil {
			return err
		}
		err = s.Tx(ctx, func(tx *sql.Tx) error {
			for _, stmt := range splitSQL(string(body)) {
				if _, err := tx.ExecContext(ctx, stmt); err != nil {
					return fmt.Errorf("%s: %w\n%s", n, err, stmt)
				}
			}
			_, err := tx.ExecContext(ctx, `INSERT INTO schema_migrations(name, applied_at) VALUES(?,?)`, n, ts(s.Now()))
			return err
		})
		if err != nil {
			return err
		}
	}
	return nil
}

func splitSQL(body string) []string {
	var out []string
	var lines []string
	for _, l := range strings.Split(body, "\n") {
		if t := strings.TrimSpace(l); strings.HasPrefix(t, "--") {
			continue
		} else if i := strings.Index(l, " -- "); i >= 0 {
			l = l[:i]
		}
		lines = append(lines, l)
	}
	for _, stmt := range strings.Split(strings.Join(lines, "\n"), ";") {
		if strings.TrimSpace(stmt) != "" {
			out = append(out, stmt)
		}
	}
	return out
}

// Tx runs fn in a transaction, committing on nil error.
func (s *Store) Tx(ctx context.Context, fn func(tx *sql.Tx) error) error {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	if err := fn(tx); err != nil {
		_ = tx.Rollback()
		return err
	}
	return tx.Commit()
}

// NewID returns a prefixed random id such as "app_3f9a...".
func NewID(prefix string) string {
	var b [12]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err)
	}
	return prefix + "_" + hex.EncodeToString(b[:])
}

func ts(t time.Time) string { return t.UTC().Format(time.RFC3339Nano) }

func tsPtr(t *time.Time) any {
	if t == nil {
		return nil
	}
	return ts(*t)
}

func parseTS(s sql.NullString) *time.Time {
	if !s.Valid || s.String == "" {
		return nil
	}
	t, err := time.Parse(time.RFC3339Nano, s.String)
	if err != nil {
		return nil
	}
	return &t
}

func mustTS(s string) time.Time {
	t, _ := time.Parse(time.RFC3339Nano, s)
	return t
}

func js(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return string(b)
}

type execer interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
}

// Event appends an execution event (used inside workflow transactions and standalone).
func (s *Store) Event(ctx context.Context, x execer, appID, kind string, from, to string, detail any) error {
	var app any
	if appID != "" {
		app = appID
	}
	var f, t any
	if from != "" {
		f = from
	}
	if to != "" {
		t = to
	}
	if detail == nil {
		detail = map[string]any{}
	}
	_, err := x.ExecContext(ctx, `INSERT INTO execution_events(application_id, kind, from_state, to_state, detail, created_at) VALUES(?,?,?,?,?,?)`,
		app, kind, f, t, js(detail), ts(s.Now()))
	return err
}
