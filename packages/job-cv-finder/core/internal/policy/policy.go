// Package policy decides, deterministically and explainably, how (or whether) a prepared
// application may be submitted. It is the only place that can choose automated execution;
// models and agents produce inputs to it but never outputs.
package policy

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/TuanKietTran/llm-toolkit/packages/job-cv-finder/core/internal/domain"
	"github.com/TuanKietTran/llm-toolkit/packages/job-cv-finder/core/internal/sources"
	"github.com/TuanKietTran/llm-toolkit/packages/job-cv-finder/core/internal/store"
)

// Input is everything the policy looks at. The workflow assembles it from the store.
type Input struct {
	Job               domain.Job
	Source            store.Source
	Account           *store.Account
	SnapshotReceived  time.Time
	Now               time.Time
	Recommendation    string
	Eligibility       string
	Documents         []store.Document
	Answers           []store.Answer
	Receipts          []store.Receipt
	SubmittedToday    int
	ExecutorAvailable map[domain.AccessMethod]bool
}

// Config holds tunables.
type Config struct {
	MaxSnapshotAge time.Duration // candidate data older than this must be re-sent from Ruxt
}

// Decision is the policy outcome plus the executor to use when automated.
type Decision struct {
	domain.PolicyDecision
	Method domain.AccessMethod
}

// Evaluate applies the rules in order. Every rule that fires adds a human-readable reason;
// the first blocking rule wins but all reasons are kept for the audit trail.
func Evaluate(in Input, cfg Config) Decision {
	if cfg.MaxSnapshotAge == 0 {
		cfg.MaxSnapshotAge = 30 * 24 * time.Hour
	}
	d := Decision{PolicyDecision: domain.PolicyDecision{Rules: sources.RulesVersion}}
	block := func(format string, args ...any) Decision {
		d.Outcome = domain.PolicyBlock
		d.Reasons = append(d.Reasons, fmt.Sprintf(format, args...))
		return d
	}

	if len(in.Receipts) > 0 {
		return block("already submitted: a receipt (%s) exists for this application", in.Receipts[0].Kind)
	}
	if !in.Source.Enabled {
		return block("source %s is disabled", in.Source.ID)
	}
	if in.Job.Status == "expired" {
		return block("job listing is marked expired")
	}
	if in.Recommendation != "strong_match" && in.Recommendation != "match" && in.Recommendation != "override" {
		return block("match recommendation %q is below the submission threshold", in.Recommendation)
	}
	if in.Eligibility == "ineligible" {
		return block("candidate is ineligible for a hard requirement")
	}
	if in.Now.Sub(in.SnapshotReceived) > cfg.MaxSnapshotAge {
		return block("candidate data from Ruxt is older than %s; send a fresh snapshot", cfg.MaxSnapshotAge)
	}
	hasCV := false
	for _, doc := range in.Documents {
		var v struct {
			Passed bool `json:"passed"`
		}
		if err := json.Unmarshal(doc.Validation, &v); err != nil || !v.Passed {
			return block("generated %s failed evidence validation", doc.Kind)
		}
		if doc.Kind == "tailored_cv" {
			hasCV = true
		}
	}
	if !hasCV {
		return block("no validated CV document prepared")
	}
	for _, a := range in.Answers {
		if a.Required && a.Status != "answered" {
			return block("required question %q has no evidence-backed answer; the candidate must provide it", a.Question)
		}
	}

	// Choose the submission method from the source's explicit capability grants.
	automatedOK := false
	handoffOK := false
	for _, g := range in.Source.Policy.Capabilities {
		if g.Capability != domain.CapSubmitApplication || !g.Permitted {
			continue
		}
		switch g.Method {
		case domain.MethodAuthorizedAPI:
			automatedOK = true
		case domain.MethodHandoff:
			handoffOK = true
		case domain.MethodBrowser:
			d.Reasons = append(d.Reasons, "browser submission is granted but no browser executor is registered")
		}
	}
	if automatedOK && !in.Source.Policy.AutomatedSubmit {
		d.Reasons = append(d.Reasons, "source policy has automated_submit=false")
		automatedOK = false
	}
	if automatedOK && !in.ExecutorAvailable[domain.MethodAuthorizedAPI] {
		d.Reasons = append(d.Reasons, "no authorized-API executor registered")
		automatedOK = false
	}
	if automatedOK {
		if in.Account == nil || !in.Account.HasSecret {
			return block("source %s needs a connected platform account with a credential", in.Source.ID)
		}
		if in.Account.Status == "reauth_required" {
			return block("platform account for %s requires re-authentication", in.Source.ID)
		}
		if lim := in.Source.Policy.DailySubmitLimit; lim > 0 && in.SubmittedToday >= lim {
			return block("daily submission limit %d reached for %s", lim, in.Source.ID)
		}
		d.Outcome = domain.PolicyAutomated
		d.Method = domain.MethodAuthorizedAPI
		d.Reasons = append(d.Reasons, "source permits submission through an authorized API; all checks passed")
		return d
	}
	if handoffOK {
		d.Outcome = domain.PolicyHandoff
		d.Method = domain.MethodHandoff
		d.Reasons = append(d.Reasons, fmt.Sprintf("automated submission is not permitted for %s; prepared for the candidate to submit", in.Source.Name))
		return d
	}
	return block("source %s permits no submission method", in.Source.ID)
}
