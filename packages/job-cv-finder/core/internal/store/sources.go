package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"

	"github.com/TuanKietTran/llm-toolkit/packages/job-cv-finder/core/internal/domain"
)

// Source is a configured job source with its explicit compliance policy.
type Source struct {
	ID            string              `json:"id"`
	Kind          string              `json:"kind"`
	Name          string              `json:"name"`
	Config        map[string]string   `json:"config"`
	Policy        domain.SourcePolicy `json:"policy"`
	Enabled       bool                `json:"enabled"`
	Health        string              `json:"health"`
	LastSuccessAt *string             `json:"last_success_at"`
	LastError     *string             `json:"last_error"`
}

// UpsertSource inserts or replaces a source definition, keeping health fields.
func (s *Store) UpsertSource(ctx context.Context, src Source) error {
	if src.Config == nil {
		src.Config = map[string]string{}
	}
	_, err := s.DB.ExecContext(ctx, `INSERT INTO job_sources(id, kind, name, config_json, policy_json, enabled, updated_at)
		VALUES(?,?,?,?,?,?,?)
		ON CONFLICT(id) DO UPDATE SET kind=excluded.kind, name=excluded.name, config_json=excluded.config_json,
		  policy_json=excluded.policy_json, enabled=excluded.enabled, updated_at=excluded.updated_at`,
		src.ID, src.Kind, src.Name, js(src.Config), js(src.Policy), boolInt(src.Enabled), ts(s.Now()))
	return err
}

// SeedSource installs or refreshes a built-in source definition (policy/config from code)
// while preserving the candidate's enabled/disabled choice.
func (s *Store) SeedSource(ctx context.Context, src Source) error {
	if src.Config == nil {
		src.Config = map[string]string{}
	}
	_, err := s.DB.ExecContext(ctx, `INSERT INTO job_sources(id, kind, name, config_json, policy_json, enabled, updated_at)
		VALUES(?,?,?,?,?,?,?)
		ON CONFLICT(id) DO UPDATE SET kind=excluded.kind, name=excluded.name, config_json=excluded.config_json,
		  policy_json=excluded.policy_json, updated_at=excluded.updated_at`,
		src.ID, src.Kind, src.Name, js(src.Config), js(src.Policy), boolInt(src.Enabled), ts(s.Now()))
	return err
}

// SetSourceEnabled toggles a source.
func (s *Store) SetSourceEnabled(ctx context.Context, id string, enabled bool) error {
	res, err := s.DB.ExecContext(ctx, `UPDATE job_sources SET enabled=?, updated_at=? WHERE id=?`, boolInt(enabled), ts(s.Now()), id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// RecordSourceHealth stores the outcome of the latest discovery attempt.
func (s *Store) RecordSourceHealth(ctx context.Context, id string, runErr error) error {
	if runErr == nil {
		_, err := s.DB.ExecContext(ctx, `UPDATE job_sources SET health='ok', last_success_at=?, last_error=NULL WHERE id=?`, ts(s.Now()), id)
		return err
	}
	_, err := s.DB.ExecContext(ctx, `UPDATE job_sources SET health='failing', last_error=? WHERE id=?`, runErr.Error(), id)
	return err
}

const sourceCols = `id, kind, name, config_json, policy_json, enabled, health, last_success_at, last_error`

func scanSource(row interface{ Scan(...any) error }) (Source, error) {
	var src Source
	var cfg, pol string
	var en int
	var last, lerr sql.NullString
	if err := row.Scan(&src.ID, &src.Kind, &src.Name, &cfg, &pol, &en, &src.Health, &last, &lerr); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return src, ErrNotFound
		}
		return src, err
	}
	_ = json.Unmarshal([]byte(cfg), &src.Config)
	_ = json.Unmarshal([]byte(pol), &src.Policy)
	src.Enabled = en == 1
	if last.Valid {
		src.LastSuccessAt = &last.String
	}
	if lerr.Valid {
		src.LastError = &lerr.String
	}
	return src, nil
}

// GetSource loads one source.
func (s *Store) GetSource(ctx context.Context, id string) (Source, error) {
	return scanSource(s.DB.QueryRowContext(ctx, `SELECT `+sourceCols+` FROM job_sources WHERE id=?`, id))
}

// ListSources returns all sources ordered by id.
func (s *Store) ListSources(ctx context.Context) ([]Source, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT `+sourceCols+` FROM job_sources ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Source
	for rows.Next() {
		src, err := scanSource(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, src)
	}
	return out, rows.Err()
}

// Account is the single platform account for one source. Secrets are referenced, never stored here.
type Account struct {
	ID          string       `json:"id"`
	SourceID    string       `json:"source_id"`
	Label       string       `json:"label"`
	SecretRef   *string      `json:"-"`
	HasSecret   bool         `json:"has_secret"`
	Status      string       `json:"status"`
	LastAuthAt  *string      `json:"last_auth_at"`
	SessionInfo *SessionInfo `json:"session,omitempty"`
}

// SessionInfo is the redacted view of the newest session for an account.
type SessionInfo struct {
	Status        string  `json:"status"`
	ExpiresAt     *string `json:"expires_at"`
	LastCheckedAt *string `json:"last_checked_at"`
	InvalidReason *string `json:"invalid_reason"`
}

// UpsertAccount creates or updates the one account for a source.
func (s *Store) UpsertAccount(ctx context.Context, a Account) error {
	_, err := s.DB.ExecContext(ctx, `INSERT INTO platform_accounts(id, source_id, label, secret_ref, status, last_auth_at, updated_at)
		VALUES(?,?,?,?,?,?,?)
		ON CONFLICT(source_id) DO UPDATE SET label=excluded.label, secret_ref=COALESCE(excluded.secret_ref, platform_accounts.secret_ref),
		  status=excluded.status, last_auth_at=COALESCE(excluded.last_auth_at, platform_accounts.last_auth_at), updated_at=excluded.updated_at`,
		a.ID, a.SourceID, a.Label, a.SecretRef, a.Status, a.LastAuthAt, ts(s.Now()))
	return err
}

// GetAccountBySource returns the account for a source.
func (s *Store) GetAccountBySource(ctx context.Context, sourceID string) (Account, error) {
	rows, err := s.listAccounts(ctx, `WHERE a.source_id=?`, sourceID)
	if err != nil {
		return Account{}, err
	}
	if len(rows) == 0 {
		return Account{}, ErrNotFound
	}
	return rows[0], nil
}

// ListAccounts returns every account with its latest session state (redacted).
func (s *Store) ListAccounts(ctx context.Context) ([]Account, error) {
	return s.listAccounts(ctx, ``)
}

func (s *Store) listAccounts(ctx context.Context, where string, args ...any) ([]Account, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT a.id, a.source_id, a.label, a.secret_ref, a.status, a.last_auth_at,
		  (SELECT status FROM account_sessions x WHERE x.account_id=a.id ORDER BY created_at DESC LIMIT 1),
		  (SELECT expires_at FROM account_sessions x WHERE x.account_id=a.id ORDER BY created_at DESC LIMIT 1),
		  (SELECT last_checked_at FROM account_sessions x WHERE x.account_id=a.id ORDER BY created_at DESC LIMIT 1),
		  (SELECT invalid_reason FROM account_sessions x WHERE x.account_id=a.id ORDER BY created_at DESC LIMIT 1)
		FROM platform_accounts a `+where+` ORDER BY a.source_id`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Account
	for rows.Next() {
		var a Account
		var ref, last, sst, sexp, schk, sinv sql.NullString
		if err := rows.Scan(&a.ID, &a.SourceID, &a.Label, &ref, &a.Status, &last, &sst, &sexp, &schk, &sinv); err != nil {
			return nil, err
		}
		if ref.Valid {
			a.SecretRef = &ref.String
			a.HasSecret = true
		}
		if last.Valid {
			a.LastAuthAt = &last.String
		}
		if sst.Valid {
			a.SessionInfo = &SessionInfo{Status: sst.String, ExpiresAt: nullStr(sexp), LastCheckedAt: nullStr(schk), InvalidReason: nullStr(sinv)}
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// RecordSession appends a session state observation for an account.
func (s *Store) RecordSession(ctx context.Context, accountID, status string, expiresAt *string, reason string) error {
	var r any
	if reason != "" {
		r = reason
	}
	now := ts(s.Now())
	_, err := s.DB.ExecContext(ctx, `INSERT INTO account_sessions(id, account_id, status, created_at, expires_at, last_checked_at, invalid_reason) VALUES(?,?,?,?,?,?,?)`,
		NewID("ses"), accountID, status, now, expiresAt, now, r)
	if err != nil {
		return err
	}
	acct := "connected"
	if status != "valid" {
		acct = "reauth_required"
	}
	_, err = s.DB.ExecContext(ctx, `UPDATE platform_accounts SET status=?, updated_at=? WHERE id=?`, acct, now, accountID)
	return err
}

func nullStr(n sql.NullString) *string {
	if !n.Valid {
		return nil
	}
	return &n.String
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
