package policy

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/TuanKietTran/llm-toolkit/packages/job-cv-finder/core/internal/domain"
	"github.com/TuanKietTran/llm-toolkit/packages/job-cv-finder/core/internal/sources"
	"github.com/TuanKietTran/llm-toolkit/packages/job-cv-finder/core/internal/store"
)

var now = time.Date(2026, 11, 20, 0, 0, 0, 0, time.UTC)

func passed() json.RawMessage { return json.RawMessage(`{"passed":true,"issues":[]}`) }

func base(src store.Source) Input {
	ans := "1 month"
	ref := "secret:x"
	return Input{
		Job: domain.Job{Status: "active"}, Source: src,
		Account:          &store.Account{SecretRef: &ref, HasSecret: true, Status: "connected"},
		SnapshotReceived: now.Add(-time.Hour), Now: now, Recommendation: "match", Eligibility: "eligible",
		Documents:         []store.Document{{Kind: "tailored_cv", Validation: passed()}},
		Answers:           []store.Answer{{Question: "Notice?", Required: true, Status: "answered", Answer: &ans}},
		ExecutorAvailable: map[domain.AccessMethod]bool{domain.MethodAuthorizedAPI: true},
	}
}

func source(id string) store.Source {
	for _, s := range sources.Defaults() {
		if s.ID == id {
			return s
		}
	}
	panic(id)
}

func TestPolicy(t *testing.T) {
	mock := sources.MockATS("http://x", "acme")
	cases := []struct {
		name    string
		mutate  func(*Input)
		outcome string
		reason  string
	}{
		{"mock ATS automated", func(*Input) {}, domain.PolicyAutomated, "authorized API"},
		{"linkedin is handoff", func(in *Input) { in.Source = source("linkedin") }, domain.PolicyHandoff, "not permitted"},
		{"itviec is handoff", func(in *Input) { in.Source = source("itviec") }, domain.PolicyHandoff, "not permitted"},
		{"greenhouse is handoff for a candidate", func(in *Input) { in.Source = sources.GreenhouseBoard("a", "A") }, domain.PolicyHandoff, ""},
		{"already submitted", func(in *Input) { in.Receipts = []store.Receipt{{Kind: "email_confirmation"}} }, domain.PolicyBlock, "already submitted"},
		{"missing required answer", func(in *Input) { in.Answers[0].Status = "unknown" }, domain.PolicyBlock, "Notice?"},
		{"failed validation", func(in *Input) { in.Documents[0].Validation = json.RawMessage(`{"passed":false}`) }, domain.PolicyBlock, "validation"},
		{"no cv", func(in *Input) { in.Documents = nil }, domain.PolicyBlock, "no validated CV"},
		{"weak match", func(in *Input) { in.Recommendation = "weak_match" }, domain.PolicyBlock, "threshold"},
		{"ineligible", func(in *Input) { in.Eligibility = "ineligible" }, domain.PolicyBlock, "ineligible"},
		{"stale candidate data", func(in *Input) { in.SnapshotReceived = now.Add(-40 * 24 * time.Hour) }, domain.PolicyBlock, "older than"},
		{"no account", func(in *Input) { in.Account = nil }, domain.PolicyBlock, "connected platform account"},
		{"reauth", func(in *Input) { in.Account.Status = "reauth_required" }, domain.PolicyBlock, "re-authentication"},
		{"daily limit", func(in *Input) { in.SubmittedToday = 20 }, domain.PolicyBlock, "daily submission limit"},
		{"disabled source", func(in *Input) { in.Source.Enabled = false }, domain.PolicyBlock, "disabled"},
		{"no executor falls back to block (mock has no handoff)", func(in *Input) { in.ExecutorAvailable = nil }, domain.PolicyBlock, "permits no submission"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			in := base(mock)
			c.mutate(&in)
			d := Evaluate(in, Config{})
			if d.Outcome != c.outcome {
				t.Fatalf("outcome %s, want %s (%v)", d.Outcome, c.outcome, d.Reasons)
			}
			if c.reason != "" && !strings.Contains(strings.Join(d.Reasons, "|"), c.reason) {
				t.Fatalf("reasons %v missing %q", d.Reasons, c.reason)
			}
			if d.Rules == "" {
				t.Fatal("rules version not recorded")
			}
		})
	}
}
