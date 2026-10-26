// Package store persists configuration, OAuth tokens and usage in SQLite.
package store

import (
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"time"

	_ "modernc.org/sqlite"

	"gorouter/internal/oauth"
	"gorouter/internal/router"
	"gorouter/internal/usage"
)

type Store struct{ db *sql.DB }

func Open(path string) (*Store, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", "file:"+path+"?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)")
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1) // SQLite: single writer, keeps things simple and race-free
	s := &Store{db: db}
	return s, s.migrate()
}

func (s *Store) Close() error { return s.db.Close() }

func (s *Store) migrate() error {
	_, err := s.db.Exec(`
CREATE TABLE IF NOT EXISTS kv (key TEXT PRIMARY KEY, value TEXT NOT NULL, updated_at INTEGER NOT NULL);
CREATE TABLE IF NOT EXISTS oauth_tokens (
  provider TEXT PRIMARY KEY, access_token TEXT NOT NULL, refresh_token TEXT NOT NULL DEFAULT '',
  expires_at INTEGER NOT NULL DEFAULT 0, updated_at INTEGER NOT NULL);
CREATE TABLE IF NOT EXISTS quota_windows (
  key TEXT NOT NULL, idx INTEGER NOT NULL, start_ns INTEGER NOT NULL, dur_ns INTEGER NOT NULL,
  requests INTEGER NOT NULL, tokens INTEGER NOT NULL, PRIMARY KEY (key, idx));
CREATE TABLE IF NOT EXISTS usage_stats (key TEXT PRIMARY KEY, stat TEXT NOT NULL);`)
	return err
}

// ---- config document (raw JSON, validated by config.Parse) ----

// RawConfig returns the stored config document, or (nil, nil) if none.
func (s *Store) RawConfig() ([]byte, error) {
	var v string
	err := s.db.QueryRow(`SELECT value FROM kv WHERE key='config'`).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return []byte(v), err
}

func (s *Store) SaveRawConfig(b []byte) error {
	_, err := s.db.Exec(`INSERT INTO kv(key,value,updated_at) VALUES('config',?,?)
ON CONFLICT(key) DO UPDATE SET value=excluded.value, updated_at=excluded.updated_at`, string(b), time.Now().Unix())
	return err
}

// ---- oauth.TokenStore ----

func (s *Store) GetToken(provider string) (oauth.Token, bool, error) {
	var t oauth.Token
	var exp int64
	err := s.db.QueryRow(`SELECT access_token, refresh_token, expires_at FROM oauth_tokens WHERE provider=?`, provider).
		Scan(&t.AccessToken, &t.RefreshToken, &exp)
	if errors.Is(err, sql.ErrNoRows) {
		return t, false, nil
	}
	if exp > 0 {
		t.Expiry = time.Unix(exp, 0)
	}
	return t, err == nil, err
}

func (s *Store) SaveToken(provider string, t oauth.Token) error {
	var exp int64
	if !t.Expiry.IsZero() {
		exp = t.Expiry.Unix()
	}
	_, err := s.db.Exec(`INSERT INTO oauth_tokens(provider,access_token,refresh_token,expires_at,updated_at) VALUES(?,?,?,?,?)
ON CONFLICT(provider) DO UPDATE SET access_token=excluded.access_token, refresh_token=excluded.refresh_token,
 expires_at=excluded.expires_at, updated_at=excluded.updated_at`, provider, t.AccessToken, t.RefreshToken, exp, time.Now().Unix())
	return err
}

func (s *Store) DeleteToken(provider string) error {
	_, err := s.db.Exec(`DELETE FROM oauth_tokens WHERE provider=?`, provider)
	return err
}

// ---- usage.Persister ----

func (s *Store) LoadUsage() (map[string]*usage.Stat, error) {
	rows, err := s.db.Query(`SELECT key, stat FROM usage_stats`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]*usage.Stat{}
	for rows.Next() {
		var k, v string
		if err := rows.Scan(&k, &v); err != nil {
			return nil, err
		}
		var st usage.Stat
		if json.Unmarshal([]byte(v), &st) == nil {
			out[k] = &st
		}
	}
	return out, rows.Err()
}

func (s *Store) SaveUsage(m map[string]*usage.Stat) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for k, st := range m {
		b, _ := json.Marshal(st)
		if _, err := tx.Exec(`INSERT INTO usage_stats(key,stat) VALUES(?,?) ON CONFLICT(key) DO UPDATE SET stat=excluded.stat`, k, string(b)); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// ---- router.QuotaPersister ----

func (s *Store) LoadQuotas() (map[string][]router.Window, error) {
	rows, err := s.db.Query(`SELECT key, idx, start_ns, dur_ns, requests, tokens FROM quota_windows ORDER BY key, idx`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string][]router.Window{}
	for rows.Next() {
		var k string
		var idx, start, dur int64
		var w router.Window
		if err := rows.Scan(&k, &idx, &start, &dur, &w.Requests, &w.Tokens); err != nil {
			return nil, err
		}
		w.Dur = time.Duration(dur)
		if start > 0 {
			w.Start = time.Unix(0, start)
		}
		out[k] = append(out[k], w)
	}
	return out, rows.Err()
}

// SaveQuotas replaces all saved windows with m.
func (s *Store) SaveQuotas(m map[string][]router.Window) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`DELETE FROM quota_windows`); err != nil {
		return err
	}
	for k, ws := range m {
		for i, w := range ws {
			var start int64
			if !w.Start.IsZero() {
				start = w.Start.UnixNano()
			}
			if _, err := tx.Exec(`INSERT INTO quota_windows(key,idx,start_ns,dur_ns,requests,tokens) VALUES(?,?,?,?,?,?)`,
				k, i, start, int64(w.Dur), w.Requests, w.Tokens); err != nil {
				return err
			}
		}
	}
	return tx.Commit()
}
