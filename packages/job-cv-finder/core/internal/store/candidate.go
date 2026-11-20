package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
)

// CVRef identifies one Ruxt CV at an exact revision.
type CVRef struct {
	CVID     string `json:"cv_id"`
	Revision int    `json:"revision"`
	Title    string `json:"title,omitempty"`
}

// CandidateSnapshot is the stored, read-only copy of Ruxt data.
type CandidateSnapshot struct {
	ID               string          `json:"id"`
	RuxtProfileID    string          `json:"ruxt_profile_id"`
	ProfileUpdatedAt string          `json:"profile_updated_at"`
	CVs              []CVRef         `json:"cvs"`
	Payload          json.RawMessage `json:"payload,omitempty"`
	ReceivedAt       string          `json:"received_at"`
	Created          bool            `json:"created"`
}

// SaveCandidateSnapshot stores an already-validated payload. Re-sending identical content
// returns the existing snapshot (idempotent).
func (s *Store) SaveCandidateSnapshot(ctx context.Context, profileID, profileUpdatedAt string, cvs []CVRef, payload []byte) (CandidateSnapshot, error) {
	sum := sha256.Sum256(payload)
	h := hex.EncodeToString(sum[:])
	snap := CandidateSnapshot{RuxtProfileID: profileID, ProfileUpdatedAt: profileUpdatedAt, CVs: cvs}
	err := s.Tx(ctx, func(tx *sql.Tx) error {
		var id, rec string
		err := tx.QueryRowContext(ctx, `SELECT id, received_at FROM candidate_snapshots WHERE content_sha256=?`, h).Scan(&id, &rec)
		if err == nil {
			snap.ID, snap.ReceivedAt = id, rec
			return nil
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		snap.ID = NewID("cand")
		snap.ReceivedAt = ts(s.Now())
		snap.Created = true
		if _, err := tx.ExecContext(ctx, `INSERT INTO candidate_snapshots(id, content_sha256, ruxt_profile_id, profile_updated_at, cv_refs, payload, received_at) VALUES(?,?,?,?,?,?,?)`,
			snap.ID, h, profileID, profileUpdatedAt, js(cvs), string(payload), snap.ReceivedAt); err != nil {
			return err
		}
		return s.Event(ctx, tx, "", "candidate.snapshot_received", "", "", map[string]any{"snapshot_id": snap.ID, "cvs": cvs})
	})
	return snap, err
}

// LatestCandidateSnapshot returns the newest snapshot (with payload).
func (s *Store) LatestCandidateSnapshot(ctx context.Context) (CandidateSnapshot, error) {
	return s.candidate(ctx, `ORDER BY received_at DESC LIMIT 1`)
}

// GetCandidateSnapshot loads a snapshot by id (with payload).
func (s *Store) GetCandidateSnapshot(ctx context.Context, id string) (CandidateSnapshot, error) {
	return s.candidate(ctx, `WHERE id=?`, id)
}

func (s *Store) candidate(ctx context.Context, tail string, args ...any) (CandidateSnapshot, error) {
	var c CandidateSnapshot
	var refs, payload string
	err := s.DB.QueryRowContext(ctx, `SELECT id, ruxt_profile_id, profile_updated_at, cv_refs, payload, received_at FROM candidate_snapshots `+tail, args...).
		Scan(&c.ID, &c.RuxtProfileID, &c.ProfileUpdatedAt, &refs, &payload, &c.ReceivedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return c, ErrNotFound
	}
	if err != nil {
		return c, err
	}
	_ = json.Unmarshal([]byte(refs), &c.CVs)
	c.Payload = json.RawMessage(payload)
	return c, nil
}

// Fact mirrors the contract's candidate fact.
type Fact struct {
	FactID             string  `json:"fact_id"`
	Category           string  `json:"category"`
	Value              string  `json:"value"`
	SourceDocumentID   string  `json:"source_document_id"`
	SourceVersionID    string  `json:"source_version_id"`
	EvidenceLocation   string  `json:"evidence_location"`
	VerificationStatus string  `json:"verification_status"`
	Confidence         float64 `json:"confidence"`
}

// SaveFacts upserts facts for a snapshot (facts are deterministic per snapshot).
func (s *Store) SaveFacts(ctx context.Context, tx *sql.Tx, snapshotID string, facts []Fact) error {
	for _, f := range facts {
		if _, err := tx.ExecContext(ctx, `INSERT INTO candidate_facts(fact_id, snapshot_id, category, value, source_document_id, source_version_id, evidence_location, verification_status, confidence, last_updated_at)
			VALUES(?,?,?,?,?,?,?,?,?,?) ON CONFLICT(snapshot_id, fact_id) DO NOTHING`,
			f.FactID, snapshotID, f.Category, f.Value, f.SourceDocumentID, f.SourceVersionID, f.EvidenceLocation, f.VerificationStatus, f.Confidence, ts(s.Now())); err != nil {
			return err
		}
	}
	return nil
}

// FactIDs returns the set of fact ids known for a snapshot.
func (s *Store) FactIDs(ctx context.Context, snapshotID string) (map[string]bool, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT fact_id FROM candidate_facts WHERE snapshot_id=?`, snapshotID)
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

// ListFacts returns facts for a snapshot.
func (s *Store) ListFacts(ctx context.Context, snapshotID string) ([]Fact, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT fact_id, category, value, source_document_id, source_version_id, evidence_location, verification_status, confidence
		FROM candidate_facts WHERE snapshot_id=? ORDER BY category, fact_id`, snapshotID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Fact
	for rows.Next() {
		var f Fact
		if err := rows.Scan(&f.FactID, &f.Category, &f.Value, &f.SourceDocumentID, &f.SourceVersionID, &f.EvidenceLocation, &f.VerificationStatus, &f.Confidence); err != nil {
			return nil, err
		}
		out = append(out, f)
	}
	return out, rows.Err()
}
