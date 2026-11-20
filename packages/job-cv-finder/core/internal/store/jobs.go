package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"

	"github.com/TuanKietTran/llm-toolkit/packages/job-cv-finder/core/internal/domain"
	"github.com/TuanKietTran/llm-toolkit/packages/job-cv-finder/core/internal/normalize"
)

// IngestOutcome says what happened to one discovered job.
type IngestOutcome string

const (
	IngestNew       IngestOutcome = "new"
	IngestChanged   IngestOutcome = "changed"
	IngestSeen      IngestOutcome = "seen"
	IngestDuplicate IngestOutcome = "duplicate" // new source row, but the same real job exists elsewhere
)

// IngestJob persists one normalized job plus its raw snapshot, deduplicating:
//  1. same (source, external id): update last_seen; content change -> status changed + new snapshot.
//  2. otherwise same canonical key on another row: insert with duplicate_of = that row's primary.
//  3. otherwise same company and similar title: insert, flag probable_duplicate_of (no merge).
//
// Historical rows are never deleted.
func (s *Store) IngestJob(ctx context.Context, j domain.Job, rawType string, raw []byte) (domain.Job, IngestOutcome, error) {
	var outcome IngestOutcome
	err := s.Tx(ctx, func(tx *sql.Tx) error {
		snapID, err := s.snapshot(ctx, tx, j, rawType, raw)
		if err != nil {
			return err
		}
		j.SourceSnapshotID = snapID

		existing, err := getJob(ctx, tx, `WHERE source_id=? AND external_job_id=?`, j.SourceID, j.ExternalJobID)
		switch {
		case err == nil:
			j.ID = existing.ID
			j.DuplicateOf = existing.DuplicateOf
			j.CollectedAt = existing.CollectedAt
			if existing.ContentHash == j.ContentHash {
				outcome = IngestSeen
				_, err = tx.ExecContext(ctx, `UPDATE jobs SET last_seen_at=?, status=CASE WHEN status='expired' THEN 'active' ELSE status END WHERE id=?`, ts(j.LastSeenAt), j.ID)
				j = existing
				return err
			}
			// A partial email summary must never overwrite a full description we already hold.
			if j.Completeness == "partial" && existing.Completeness == "full" {
				outcome = IngestSeen
				_, err = tx.ExecContext(ctx, `UPDATE jobs SET last_seen_at=? WHERE id=?`, ts(j.LastSeenAt), j.ID)
				j = existing
				return err
			}
			outcome = IngestChanged
			j.Status = "changed"
			return updateJob(ctx, tx, j)
		case errors.Is(err, ErrNotFound):
		default:
			return err
		}

		j.ID = NewID("job")
		primary, err := getJob(ctx, tx, `WHERE canonical_key=? AND duplicate_of IS NULL ORDER BY collected_at LIMIT 1`, j.CanonicalKey)
		switch {
		case err == nil:
			j.DuplicateOf = primary.ID
			outcome = IngestDuplicate
		case errors.Is(err, ErrNotFound):
			outcome = IngestNew
			// Secondary signal only: flag, never merge.
			cands, err := listJobs(ctx, tx, `WHERE company_norm=? AND duplicate_of IS NULL LIMIT 50`, j.CompanyNorm)
			if err != nil {
				return err
			}
			for _, c := range cands {
				if normalize.Similar(c, j) {
					j.DuplicateOf = ""
					if err := insertJob(ctx, tx, j, c.ID); err != nil {
						return err
					}
					return s.Event(ctx, tx, "", "job.ingested", "", "", map[string]any{"job_id": j.ID, "outcome": outcome, "probable_duplicate_of": c.ID})
				}
			}
		default:
			return err
		}
		if err := insertJob(ctx, tx, j, ""); err != nil {
			return err
		}
		return s.Event(ctx, tx, "", "job.ingested", "", "", map[string]any{"job_id": j.ID, "outcome": outcome, "duplicate_of": j.DuplicateOf})
	})
	return j, outcome, err
}

func (s *Store) snapshot(ctx context.Context, tx *sql.Tx, j domain.Job, rawType string, raw []byte) (string, error) {
	if len(raw) == 0 {
		raw, _ = json.Marshal(j)
		rawType = "application/json"
	}
	sum := sha256.Sum256(raw)
	h := hex.EncodeToString(sum[:])
	var id string
	err := tx.QueryRowContext(ctx, `SELECT id FROM job_snapshots WHERE source_id=? AND external_job_id=? AND raw_sha256=?`, j.SourceID, j.ExternalJobID, h).Scan(&id)
	if err == nil {
		return id, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return "", err
	}
	id = NewID("snap")
	_, err = tx.ExecContext(ctx, `INSERT INTO job_snapshots(id, source_id, external_job_id, content_type, raw, raw_sha256, collected_at) VALUES(?,?,?,?,?,?,?)`,
		id, j.SourceID, j.ExternalJobID, rawType, raw, h, ts(s.Now()))
	return id, err
}

const jobCols = `id, source_id, external_job_id, canonical_key, company, company_norm, title, title_norm, description, location, work_mode,
	employment_type, salary_min, salary_max, salary_currency, required_skills, preferred_skills, experience_requirement,
	education_requirement, language_requirement, application_url, questions, published_at, collected_at, last_seen_at, content_hash,
	source_snapshot_id, duplicate_of, status, completeness`

func insertJob(ctx context.Context, tx *sql.Tx, j domain.Job, probable string) error {
	var dup, prob any
	if j.DuplicateOf != "" {
		dup = j.DuplicateOf
	}
	if probable != "" {
		prob = probable
	}
	_, err := tx.ExecContext(ctx, `INSERT INTO jobs(`+jobCols+`, probable_duplicate_of) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		j.ID, j.SourceID, j.ExternalJobID, j.CanonicalKey, j.Company, j.CompanyNorm, j.Title, j.TitleNorm, j.Description, j.Location, j.WorkMode,
		j.EmploymentType, j.SalaryMin, j.SalaryMax, j.SalaryCurrency, js(nonNil(j.RequiredSkills)), js(nonNil(j.PreferredSkills)), j.ExperienceRequirement,
		j.EducationRequirement, j.LanguageRequirement, j.ApplicationURL, js(nonNilQ(j.Questions)), tsPtr(j.PublishedAt), ts(j.CollectedAt), ts(j.LastSeenAt), j.ContentHash,
		j.SourceSnapshotID, dup, j.Status, j.Completeness, prob)
	return err
}

func updateJob(ctx context.Context, tx *sql.Tx, j domain.Job) error {
	_, err := tx.ExecContext(ctx, `UPDATE jobs SET canonical_key=?, company=?, company_norm=?, title=?, title_norm=?, description=?, location=?, work_mode=?,
		employment_type=?, salary_min=?, salary_max=?, salary_currency=?, required_skills=?, preferred_skills=?, experience_requirement=?,
		education_requirement=?, language_requirement=?, application_url=?, questions=?, published_at=?, last_seen_at=?, content_hash=?,
		source_snapshot_id=?, status=?, completeness=? WHERE id=?`,
		j.CanonicalKey, j.Company, j.CompanyNorm, j.Title, j.TitleNorm, j.Description, j.Location, j.WorkMode,
		j.EmploymentType, j.SalaryMin, j.SalaryMax, j.SalaryCurrency, js(nonNil(j.RequiredSkills)), js(nonNil(j.PreferredSkills)), j.ExperienceRequirement,
		j.EducationRequirement, j.LanguageRequirement, j.ApplicationURL, js(nonNilQ(j.Questions)), tsPtr(j.PublishedAt), ts(j.LastSeenAt), j.ContentHash,
		j.SourceSnapshotID, j.Status, j.Completeness, j.ID)
	return err
}

type querier interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

func scanJob(row interface{ Scan(...any) error }) (domain.Job, error) {
	var j domain.Job
	var smin, smax sql.NullFloat64
	var req, pref, qs string
	var pub, snap, dup sql.NullString
	var coll, seen string
	err := row.Scan(&j.ID, &j.SourceID, &j.ExternalJobID, &j.CanonicalKey, &j.Company, &j.CompanyNorm, &j.Title, &j.TitleNorm, &j.Description, &j.Location, &j.WorkMode,
		&j.EmploymentType, &smin, &smax, &j.SalaryCurrency, &req, &pref, &j.ExperienceRequirement,
		&j.EducationRequirement, &j.LanguageRequirement, &j.ApplicationURL, &qs, &pub, &coll, &seen, &j.ContentHash,
		&snap, &dup, &j.Status, &j.Completeness)
	if errors.Is(err, sql.ErrNoRows) {
		return j, ErrNotFound
	}
	if err != nil {
		return j, err
	}
	if smin.Valid {
		j.SalaryMin = &smin.Float64
	}
	if smax.Valid {
		j.SalaryMax = &smax.Float64
	}
	_ = json.Unmarshal([]byte(req), &j.RequiredSkills)
	_ = json.Unmarshal([]byte(pref), &j.PreferredSkills)
	_ = json.Unmarshal([]byte(qs), &j.Questions)
	j.PublishedAt = parseTS(pub)
	j.CollectedAt = mustTS(coll)
	j.LastSeenAt = mustTS(seen)
	j.SourceSnapshotID = snap.String
	j.DuplicateOf = dup.String
	return j, nil
}

func getJob(ctx context.Context, q querier, where string, args ...any) (domain.Job, error) {
	return scanJob(q.QueryRowContext(ctx, `SELECT `+jobCols+` FROM jobs `+where, args...))
}

func listJobs(ctx context.Context, q querier, where string, args ...any) ([]domain.Job, error) {
	rows, err := q.QueryContext(ctx, `SELECT `+jobCols+` FROM jobs `+where, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.Job
	for rows.Next() {
		j, err := scanJob(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, j)
	}
	return out, rows.Err()
}

// GetJob loads a job by id.
func (s *Store) GetJob(ctx context.Context, id string) (domain.Job, error) {
	return getJob(ctx, s.DB, `WHERE id=?`, id)
}

// FindJobByExternal looks a job up by source + external id.
func (s *Store) FindJobByExternal(ctx context.Context, sourceID, externalID string) (domain.Job, error) {
	return getJob(ctx, s.DB, `WHERE source_id=? AND external_job_id=?`, sourceID, externalID)
}

// FindJobsByCanonicalKey returns all rows (any source) for one real job.
func (s *Store) FindJobsByCanonicalKey(ctx context.Context, key string) ([]domain.Job, error) {
	return listJobs(ctx, s.DB, `WHERE canonical_key=? ORDER BY collected_at`, key)
}

// JobFilter narrows ListJobs.
type JobFilter struct {
	SourceID      string
	OnlyPrimary   bool
	Query         string
	Limit, Offset int
}

// ListJobs lists jobs newest first.
func (s *Store) ListJobs(ctx context.Context, f JobFilter) ([]domain.Job, error) {
	var where []string
	var args []any
	if f.SourceID != "" {
		where = append(where, "source_id=?")
		args = append(args, f.SourceID)
	}
	if f.OnlyPrimary {
		where = append(where, "duplicate_of IS NULL")
	}
	if f.Query != "" {
		where = append(where, "(title LIKE ? OR company LIKE ?)")
		args = append(args, "%"+f.Query+"%", "%"+f.Query+"%")
	}
	w := ""
	if len(where) > 0 {
		w = "WHERE " + strings.Join(where, " AND ")
	}
	if f.Limit <= 0 || f.Limit > 500 {
		f.Limit = 100
	}
	args = append(args, f.Limit, f.Offset)
	return listJobs(ctx, s.DB, w+` ORDER BY last_seen_at DESC LIMIT ? OFFSET ?`, args...)
}

// UpdateJobDescription enriches a partial job with a candidate-provided description.
func (s *Store) UpdateJobDescription(ctx context.Context, j domain.Job) error {
	return s.Tx(ctx, func(tx *sql.Tx) error {
		if err := updateJob(ctx, tx, j); err != nil {
			return err
		}
		return s.Event(ctx, tx, "", "job.enriched", "", "", map[string]any{"job_id": j.ID})
	})
}

// GetSnapshotRaw returns a stored raw source snapshot.
func (s *Store) GetSnapshotRaw(ctx context.Context, id string) (string, []byte, error) {
	var ct string
	var raw []byte
	err := s.DB.QueryRowContext(ctx, `SELECT content_type, raw FROM job_snapshots WHERE id=?`, id).Scan(&ct, &raw)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil, ErrNotFound
	}
	return ct, raw, err
}

func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

func nonNilQ(q []domain.Question) []domain.Question {
	if q == nil {
		return []domain.Question{}
	}
	return q
}
