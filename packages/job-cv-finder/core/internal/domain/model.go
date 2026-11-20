package domain

import "time"

// Capability is something a source adapter may be able to do. Discovery and submission
// are independent: a source that lists jobs is not thereby allowed to receive applications.
type Capability string

const (
	CapDiscoverJobs        Capability = "discoverJobs"
	CapReadJobDetails      Capability = "readJobDetails"
	CapAuthenticatedAccess Capability = "authenticatedAccess"
	CapSubmitApplication   Capability = "submitApplication"
	CapTrackApplication    Capability = "trackApplication"
)

// AccessMethod says HOW a capability is exercised, which the policy engine needs to know.
type AccessMethod string

const (
	MethodPublicAPI     AccessMethod = "public_api"     // documented, unauthenticated API (e.g. Greenhouse/Lever GET)
	MethodAuthorizedAPI AccessMethod = "authorized_api" // API with credentials the candidate legitimately holds
	MethodEmailIntake   AccessMethod = "email_intake"   // job-alert / confirmation emails the candidate receives
	MethodUserProvided  AccessMethod = "user_provided"  // the candidate pastes a URL or description
	MethodHandoff       AccessMethod = "handoff"        // the candidate performs the step on the platform
	MethodBrowser       AccessMethod = "browser"        // automated browser; only where policy records permission
)

// CapabilityGrant is one declared capability with its method and the evidence for permission.
type CapabilityGrant struct {
	Capability Capability   `json:"capability"`
	Method     AccessMethod `json:"method"`
	Permitted  bool         `json:"permitted"`
	Basis      string       `json:"basis"` // where the permission (or restriction) comes from
}

// SourcePolicy is the persisted, explicit compliance record for a source.
type SourcePolicy struct {
	TermsURL           string            `json:"terms_url"`
	TermsVerified      bool              `json:"terms_verified"` // read in full by a human/agent, with date
	TermsVerifiedOn    string            `json:"terms_verified_on,omitempty"`
	AutomatedSubmit    bool              `json:"automated_submit"`
	RateLimitPerMinute int               `json:"rate_limit_per_minute"`
	DailySubmitLimit   int               `json:"daily_submit_limit"`
	RetentionDays      int               `json:"retention_days"`
	Capabilities       []CapabilityGrant `json:"capabilities"`
	Notes              string            `json:"notes,omitempty"`
}

// Permits reports whether capability c is permitted, and by which method.
func (p SourcePolicy) Permits(c Capability) (AccessMethod, bool) {
	for _, g := range p.Capabilities {
		if g.Capability == c && g.Permitted {
			return g.Method, true
		}
	}
	return "", false
}

// Job is the canonical, normalized job representation.
type Job struct {
	ID                    string     `json:"job_id"`
	SourceID              string     `json:"source_id"`
	ExternalJobID         string     `json:"external_job_id"`
	CanonicalKey          string     `json:"canonical_key"`
	Company               string     `json:"company"`
	CompanyNorm           string     `json:"company_norm"`
	Title                 string     `json:"title"`
	TitleNorm             string     `json:"title_norm"`
	Description           string     `json:"description"`
	Location              string     `json:"location"`
	WorkMode              string     `json:"work_mode"` // onsite|hybrid|remote|unknown
	EmploymentType        string     `json:"employment_type"`
	SalaryMin             *float64   `json:"salary_min"`
	SalaryMax             *float64   `json:"salary_max"`
	SalaryCurrency        string     `json:"salary_currency"`
	RequiredSkills        []string   `json:"required_skills"`
	PreferredSkills       []string   `json:"preferred_skills"`
	ExperienceRequirement string     `json:"experience_requirement"`
	EducationRequirement  string     `json:"education_requirement"`
	LanguageRequirement   string     `json:"language_requirement"`
	ApplicationURL        string     `json:"application_url"`
	PublishedAt           *time.Time `json:"published_at"`
	CollectedAt           time.Time  `json:"collected_at"`
	LastSeenAt            time.Time  `json:"last_seen_at"`
	ContentHash           string     `json:"content_hash"`
	SourceSnapshotID      string     `json:"source_snapshot_id"`
	DuplicateOf           string     `json:"duplicate_of,omitempty"`
	Status                string     `json:"status"`       // active|changed|expired
	Completeness          string     `json:"completeness"` // full|partial (e.g. email alert without description)
	Questions             []Question `json:"questions,omitempty"`
}

// Question is an application-form question exposed by a source (ATS forms).
type Question struct {
	ID       string `json:"id"`
	Label    string `json:"label"`
	Required bool   `json:"required"`
	Type     string `json:"type"` // text|textarea|boolean|select|file
}

// RawJob is what an adapter returns before normalization.
type RawJob struct {
	ExternalJobID  string
	Company        string
	Title          string
	Description    string // may be HTML; normalization strips it
	Location       string
	EmploymentType string
	SalaryText     string
	Skills         []string
	ApplicationURL string
	PublishedAt    *time.Time
	Questions      []Question
	Partial        bool   // true when the source gave only a summary (email alerts)
	RawContentType string // e.g. application/json, message/rfc822
	Raw            []byte // preserved verbatim as the job snapshot
}

// PolicyDecision is the persisted outcome of a policy check.
type PolicyDecision struct {
	Outcome string   `json:"outcome"` // automated|handoff|block
	Reasons []string `json:"reasons"`
	Rules   string   `json:"rules_version"`
}

const (
	PolicyAutomated = "automated"
	PolicyHandoff   = "handoff"
	PolicyBlock     = "block"
)
