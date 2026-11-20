-- Job CV Finder core schema. Candidate/CV data is owned by Ruxt: candidate_snapshots is a
-- materialized, user-initiated copy with Ruxt ids + revisions, never an authoritative store.

CREATE TABLE job_sources (
  id               TEXT PRIMARY KEY,
  kind             TEXT NOT NULL,           -- greenhouse|lever|linkedin_email|itviec_email|...|mockats
  name             TEXT NOT NULL,
  config_json      TEXT NOT NULL DEFAULT '{}',
  policy_json      TEXT NOT NULL,           -- domain.SourcePolicy
  enabled          INTEGER NOT NULL DEFAULT 1,
  health           TEXT NOT NULL DEFAULT 'unknown', -- ok|degraded|failing|unknown
  last_success_at  TEXT,
  last_error       TEXT,
  updated_at       TEXT NOT NULL
);

CREATE TABLE platform_accounts (
  id               TEXT PRIMARY KEY,
  source_id        TEXT NOT NULL UNIQUE REFERENCES job_sources(id), -- one account per platform
  label            TEXT NOT NULL,
  secret_ref       TEXT,                    -- handle into secrets; never the secret itself
  status           TEXT NOT NULL DEFAULT 'not_connected', -- connected|not_connected|reauth_required
  last_auth_at     TEXT,
  updated_at       TEXT NOT NULL
);

CREATE TABLE account_sessions (
  id               TEXT PRIMARY KEY,
  account_id       TEXT NOT NULL REFERENCES platform_accounts(id),
  status           TEXT NOT NULL,           -- valid|expired|invalid
  created_at       TEXT NOT NULL,
  expires_at       TEXT,
  last_checked_at  TEXT,
  invalid_reason   TEXT
);
CREATE INDEX account_sessions_account ON account_sessions(account_id, created_at);

CREATE TABLE secrets (
  ref              TEXT PRIMARY KEY,
  nonce            BLOB NOT NULL,
  ciphertext       BLOB NOT NULL,
  key_id           TEXT NOT NULL,
  created_at       TEXT NOT NULL
);

CREATE TABLE discovery_runs (
  id               TEXT PRIMARY KEY,
  source_id        TEXT NOT NULL REFERENCES job_sources(id),
  idempotency_key  TEXT NOT NULL UNIQUE,
  state            TEXT NOT NULL,           -- SCHEDULED|DISCOVERING|COMPLETED|FAILED
  scheduled_at     TEXT NOT NULL,
  started_at       TEXT,
  finished_at      TEXT,
  found            INTEGER NOT NULL DEFAULT 0,
  new_jobs         INTEGER NOT NULL DEFAULT 0,
  changed_jobs     INTEGER NOT NULL DEFAULT 0,
  duplicates       INTEGER NOT NULL DEFAULT 0,
  error            TEXT
);
CREATE INDEX discovery_runs_source ON discovery_runs(source_id, scheduled_at);

CREATE TABLE job_snapshots (
  id               TEXT PRIMARY KEY,
  source_id        TEXT NOT NULL,
  external_job_id  TEXT NOT NULL,
  content_type     TEXT NOT NULL,
  raw              BLOB NOT NULL,
  raw_sha256       TEXT NOT NULL,
  collected_at     TEXT NOT NULL,
  UNIQUE(source_id, external_job_id, raw_sha256)
);

CREATE TABLE jobs (
  id                     TEXT PRIMARY KEY,
  source_id              TEXT NOT NULL REFERENCES job_sources(id),
  external_job_id        TEXT NOT NULL,
  canonical_key          TEXT NOT NULL,
  company                TEXT NOT NULL,
  company_norm           TEXT NOT NULL,
  title                  TEXT NOT NULL,
  title_norm             TEXT NOT NULL,
  description            TEXT NOT NULL DEFAULT '',
  location               TEXT NOT NULL DEFAULT '',
  work_mode              TEXT NOT NULL DEFAULT 'unknown',
  employment_type        TEXT NOT NULL DEFAULT '',
  salary_min             REAL,
  salary_max             REAL,
  salary_currency        TEXT NOT NULL DEFAULT '',
  required_skills        TEXT NOT NULL DEFAULT '[]',
  preferred_skills       TEXT NOT NULL DEFAULT '[]',
  experience_requirement TEXT NOT NULL DEFAULT '',
  education_requirement  TEXT NOT NULL DEFAULT '',
  language_requirement   TEXT NOT NULL DEFAULT '',
  application_url        TEXT NOT NULL DEFAULT '',
  questions              TEXT NOT NULL DEFAULT '[]',
  published_at           TEXT,
  collected_at           TEXT NOT NULL,
  last_seen_at           TEXT NOT NULL,
  content_hash           TEXT NOT NULL,
  source_snapshot_id     TEXT REFERENCES job_snapshots(id),
  duplicate_of           TEXT REFERENCES jobs(id),
  probable_duplicate_of  TEXT REFERENCES jobs(id),
  status                 TEXT NOT NULL DEFAULT 'active', -- active|changed|expired
  completeness           TEXT NOT NULL DEFAULT 'full',
  UNIQUE(source_id, external_job_id)
);
CREATE INDEX jobs_canonical ON jobs(canonical_key);
CREATE INDEX jobs_company ON jobs(company_norm);
CREATE INDEX jobs_seen ON jobs(last_seen_at);

CREATE TABLE candidate_snapshots (
  id               TEXT PRIMARY KEY,
  content_sha256   TEXT NOT NULL UNIQUE,
  ruxt_profile_id  TEXT NOT NULL,
  profile_updated_at TEXT NOT NULL,
  cv_refs          TEXT NOT NULL,          -- [{cv_id, revision}]
  payload          TEXT NOT NULL,          -- validated CandidateSnapshot JSON
  received_at      TEXT NOT NULL
);

CREATE TABLE candidate_facts (
  fact_id              TEXT NOT NULL,
  snapshot_id          TEXT NOT NULL REFERENCES candidate_snapshots(id),
  category             TEXT NOT NULL,
  value                TEXT NOT NULL,
  source_document_id   TEXT NOT NULL,
  source_version_id    TEXT NOT NULL,
  evidence_location    TEXT NOT NULL,
  verification_status  TEXT NOT NULL,
  confidence           REAL NOT NULL,
  last_updated_at      TEXT NOT NULL,
  PRIMARY KEY (snapshot_id, fact_id)
);

CREATE TABLE job_matches (
  id               TEXT PRIMARY KEY,
  job_id           TEXT NOT NULL REFERENCES jobs(id),
  snapshot_id      TEXT NOT NULL REFERENCES candidate_snapshots(id),
  match_score      REAL NOT NULL,
  eligibility      TEXT NOT NULL,
  confidence       REAL NOT NULL,
  recommendation   TEXT NOT NULL,
  result           TEXT NOT NULL,          -- validated MatchResult JSON
  created_at       TEXT NOT NULL,
  UNIQUE(job_id, snapshot_id)
);

CREATE TABLE applications (
  id                    TEXT PRIMARY KEY,
  job_id                TEXT NOT NULL REFERENCES jobs(id),
  logical_key           TEXT NOT NULL UNIQUE, -- canonical job key: one application per real job
  state                 TEXT NOT NULL,
  version               INTEGER NOT NULL DEFAULT 1,
  snapshot_id           TEXT NOT NULL REFERENCES candidate_snapshots(id),
  match_id              TEXT REFERENCES job_matches(id),
  selected_cv_id        TEXT,
  selected_cv_revision  INTEGER,
  executor              TEXT,
  idempotency_key       TEXT NOT NULL UNIQUE,
  attempts              INTEGER NOT NULL DEFAULT 0,
  next_attempt_at       TEXT,
  last_error            TEXT,
  exception_reason      TEXT,
  outcome               TEXT,
  notes                 TEXT,
  created_at            TEXT NOT NULL,
  updated_at            TEXT NOT NULL,
  submitted_at          TEXT
);
CREATE INDEX applications_state ON applications(state, next_attempt_at);

CREATE TABLE application_documents (
  id                       TEXT PRIMARY KEY,
  application_id           TEXT NOT NULL REFERENCES applications(id),
  kind                     TEXT NOT NULL,
  format                   TEXT NOT NULL,
  content                  TEXT NOT NULL,
  content_sha256           TEXT NOT NULL,
  statements               TEXT NOT NULL,
  source_cv_id             TEXT NOT NULL,
  source_cv_revision       INTEGER NOT NULL,
  job_snapshot_id          TEXT,
  engine                   TEXT NOT NULL,   -- {name, version, model_provider, model_id, prompt_version}
  validation               TEXT NOT NULL,
  ruxt_registration        TEXT NOT NULL DEFAULT 'pending', -- pending|registered|unavailable
  created_at               TEXT NOT NULL
);
CREATE INDEX application_documents_app ON application_documents(application_id);

CREATE TABLE application_answers (
  application_id   TEXT NOT NULL REFERENCES applications(id),
  question_id      TEXT NOT NULL,
  question         TEXT NOT NULL,
  required         INTEGER NOT NULL,
  status           TEXT NOT NULL,          -- answered|unknown
  answer           TEXT,
  source           TEXT NOT NULL,
  evidence_refs    TEXT NOT NULL,
  PRIMARY KEY (application_id, question_id)
);

CREATE TABLE execution_runs (
  id               TEXT PRIMARY KEY,
  application_id   TEXT NOT NULL REFERENCES applications(id),
  executor         TEXT NOT NULL,
  idempotency_key  TEXT NOT NULL,
  attempt          INTEGER NOT NULL,
  state            TEXT NOT NULL,          -- started|succeeded|failed|unknown
  started_at       TEXT NOT NULL,
  finished_at      TEXT,
  error            TEXT,
  response         TEXT
);
CREATE INDEX execution_runs_app ON execution_runs(application_id, attempt);

CREATE TABLE execution_events (
  seq              INTEGER PRIMARY KEY AUTOINCREMENT,
  application_id   TEXT,
  kind             TEXT NOT NULL,
  from_state       TEXT,
  to_state         TEXT,
  detail           TEXT NOT NULL DEFAULT '{}',
  created_at       TEXT NOT NULL
);
CREATE INDEX execution_events_app ON execution_events(application_id, seq);

CREATE TABLE submission_receipts (
  id               TEXT PRIMARY KEY,
  application_id   TEXT NOT NULL REFERENCES applications(id),
  kind             TEXT NOT NULL,          -- ats_api|email_confirmation|user_attested
  external_ref     TEXT NOT NULL,
  evidence         TEXT NOT NULL,
  verified         INTEGER NOT NULL,
  received_at      TEXT NOT NULL,
  UNIQUE(kind, external_ref)
);
CREATE INDEX submission_receipts_app ON submission_receipts(application_id);

CREATE TABLE policy_decisions (
  id               TEXT PRIMARY KEY,
  application_id   TEXT NOT NULL REFERENCES applications(id),
  outcome          TEXT NOT NULL,
  reasons          TEXT NOT NULL,
  rules_version    TEXT NOT NULL,
  created_at       TEXT NOT NULL
);

CREATE TABLE tasks (
  id               TEXT PRIMARY KEY,
  kind             TEXT NOT NULL,          -- intel.match|intel.prepare
  application_id   TEXT REFERENCES applications(id),
  idempotency_key  TEXT NOT NULL UNIQUE,
  payload          TEXT NOT NULL,
  state            TEXT NOT NULL,          -- queued|leased|done|dead
  attempts         INTEGER NOT NULL DEFAULT 0,
  max_attempts     INTEGER NOT NULL DEFAULT 5,
  lease_owner      TEXT,
  lease_until      TEXT,
  not_before       TEXT NOT NULL,
  result           TEXT,
  error            TEXT,
  created_at       TEXT NOT NULL,
  updated_at       TEXT NOT NULL
);
CREATE INDEX tasks_ready ON tasks(state, kind, not_before);

CREATE TABLE inbound_messages (
  id               TEXT PRIMARY KEY,
  message_id       TEXT NOT NULL UNIQUE,  -- RFC 5322 Message-ID: replaying an email is a no-op
  sender           TEXT NOT NULL,
  subject          TEXT NOT NULL,
  received_at      TEXT NOT NULL,
  classification   TEXT NOT NULL,         -- linkedin_alert|linkedin_confirmation|itviec_alert|itviec_confirmation|unknown
  parsed           TEXT NOT NULL,
  processed_at     TEXT NOT NULL
);
