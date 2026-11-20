// Package sources holds job-source adapters and the default, explicit compliance policy of
// each source. Discovery and submission are separate capabilities; see docs/sources/SOURCES.md
// for the evidence behind every policy below.
package sources

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/TuanKietTran/llm-toolkit/packages/job-cv-finder/core/internal/domain"
	"github.com/TuanKietTran/llm-toolkit/packages/job-cv-finder/core/internal/store"
)

// Discoverer pulls jobs from a source that permits programmatic discovery.
type Discoverer interface {
	Discover(ctx context.Context, src store.Source) ([]domain.RawJob, error)
}

// Registry maps source kinds to discovery adapters. Sources without one (email intake,
// user-provided) receive jobs through the intake API instead of polling.
type Registry struct {
	discoverers map[string]Discoverer
}

// NewRegistry builds the registry with the HTTP adapters.
func NewRegistry(client *http.Client) *Registry {
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}
	return &Registry{discoverers: map[string]Discoverer{
		KindGreenhouse: &Greenhouse{Client: client},
		KindLever:      &Lever{Client: client},
		KindMockATS:    &Greenhouse{Client: client}, // the mock ATS speaks the Greenhouse board shape
	}}
}

// Discoverer returns the adapter for a source, or an error when the source has no permitted
// programmatic discovery (the policy is checked, not just the adapter's existence).
func (r *Registry) Discoverer(src store.Source) (Discoverer, error) {
	method, ok := src.Policy.Permits(domain.CapDiscoverJobs)
	if !ok {
		return nil, fmt.Errorf("source %s: discovery not permitted by policy", src.ID)
	}
	if method != domain.MethodPublicAPI && method != domain.MethodAuthorizedAPI {
		return nil, fmt.Errorf("source %s: discovery is via %s, not polling", src.ID, method)
	}
	d, ok := r.discoverers[src.Kind]
	if !ok {
		return nil, fmt.Errorf("source %s: no adapter for kind %s", src.ID, src.Kind)
	}
	return d, nil
}

// Source kinds.
const (
	KindLinkedIn     = "linkedin"
	KindITviec       = "itviec"
	KindCareerViet   = "careerviet"
	KindVietnamWorks = "vietnamworks"
	KindGreenhouse   = "greenhouse"
	KindLever        = "lever"
	KindManual       = "manual"
	KindMockATS      = "mockats"
)

// RulesVersion tags policy decisions so old decisions stay explainable after rule changes.
const RulesVersion = "2026-11-20.1"

func grant(c domain.Capability, m domain.AccessMethod, ok bool, basis string) domain.CapabilityGrant {
	return domain.CapabilityGrant{Capability: c, Method: m, Permitted: ok, Basis: basis}
}

const (
	basisEmail   = "Emails the platform sends to the candidate's own inbox; parsing them involves no access to the platform."
	basisHandoff = "The candidate submits on the platform themselves; the system only prepares documents."
	basisNoAuto  = "Automated access is not permitted: the platform's terms were not verified to allow it (see docs/sources/SOURCES.md). Default deny."
	basisUser    = "Job URLs and descriptions the candidate pastes in."
)

// Defaults returns the built-in source definitions. Preferred: LinkedIn, ITviec. Candidates:
// CareerViet, VietnamWorks. ATS boards are added per employer (board token / site name).
func Defaults() []store.Source {
	platform := func(id, name, terms, notes string, emailParser bool) store.Source {
		caps := []domain.CapabilityGrant{
			grant(domain.CapReadJobDetails, domain.MethodUserProvided, true, basisUser),
			grant(domain.CapSubmitApplication, domain.MethodHandoff, true, basisHandoff),
			grant(domain.CapSubmitApplication, domain.MethodBrowser, false, basisNoAuto),
			grant(domain.CapAuthenticatedAccess, domain.MethodBrowser, false, basisNoAuto),
		}
		if emailParser {
			caps = append(caps,
				grant(domain.CapDiscoverJobs, domain.MethodEmailIntake, true, basisEmail),
				grant(domain.CapTrackApplication, domain.MethodEmailIntake, true, basisEmail))
		} else {
			caps = append(caps, grant(domain.CapDiscoverJobs, domain.MethodUserProvided, true, basisUser))
		}
		return store.Source{ID: id, Kind: id, Name: name, Enabled: true, Config: map[string]string{},
			Policy: domain.SourcePolicy{TermsURL: terms, TermsVerified: false, AutomatedSubmit: false, RateLimitPerMinute: 0,
				DailySubmitLimit: 0, RetentionDays: 365, Capabilities: caps, Notes: notes}}
	}
	return []store.Source{
		platform(KindLinkedIn, "LinkedIn", "https://www.linkedin.com/legal/user-agreement",
			"Preferred. Jobs arrive from job-alert emails (jobalerts-noreply@linkedin.com); submission is confirmed by 'your application was sent to' emails (jobs-noreply@linkedin.com) carrying the same job id. The user agreement restricts bots/automated access; the page could not be fetched by automation on 2026-11-20, so the clause must be re-read by a human before any automated capability is enabled.", true),
		platform(KindITviec, "ITviec", "https://itviec.com/terms-conditions",
			"Preferred. Jobs arrive from 'Job Robot' alert emails; submission is confirmed by 'ITviec đã nhận hồ sơ' / 'Chúc mừng! Hồ sơ của bạn ... đã được gửi đi!' emails. The terms page returned HTTP 403 to automated fetch on 2026-11-20; unverified.", true),
		platform(KindCareerViet, "CareerViet", "https://careerviet.vn",
			"Candidate source. Terms not yet reviewed; no alert-email parser yet. Jobs can be added by URL/description; submission by handoff only.", false),
		platform(KindVietnamWorks, "VietnamWorks", "https://www.vietnamworks.com",
			"Candidate source. Account exists (welcome email seen 2026-11). Terms not yet reviewed; no alert-email parser yet. Jobs by URL/description; submission by handoff only.", false),
		{ID: KindManual, Kind: KindManual, Name: "Manual / pasted jobs", Enabled: true, Config: map[string]string{},
			Policy: domain.SourcePolicy{TermsVerified: true, RetentionDays: 365, Capabilities: []domain.CapabilityGrant{
				grant(domain.CapDiscoverJobs, domain.MethodUserProvided, true, basisUser),
				grant(domain.CapReadJobDetails, domain.MethodUserProvided, true, basisUser),
				grant(domain.CapSubmitApplication, domain.MethodHandoff, true, basisHandoff),
			}}},
	}
}

// GreenhouseBoard defines an employer's public Greenhouse board as a discovery source.
func GreenhouseBoard(boardToken, company string) store.Source {
	return store.Source{ID: "greenhouse:" + boardToken, Kind: KindGreenhouse, Name: company + " (Greenhouse)", Enabled: true,
		Config: map[string]string{"board_token": boardToken, "base_url": "https://boards-api.greenhouse.io"},
		Policy: domain.SourcePolicy{TermsURL: "https://developers.greenhouse.io/job-board.html", TermsVerified: true, TermsVerifiedOn: "2026-11-20",
			RateLimitPerMinute: 30, RetentionDays: 365, Capabilities: []domain.CapabilityGrant{
				grant(domain.CapDiscoverJobs, domain.MethodPublicAPI, true, "Job Board API: only the application POST requires Basic Auth; GET job listings do not."),
				grant(domain.CapReadJobDetails, domain.MethodPublicAPI, true, "GET /jobs/{id}?questions=true is public."),
				grant(domain.CapSubmitApplication, domain.MethodAuthorizedAPI, false, "Application POST needs the EMPLOYER's Job Board API key, which a candidate does not hold."),
				grant(domain.CapSubmitApplication, domain.MethodHandoff, true, basisHandoff),
			}}}
}

// LeverSite defines an employer's public Lever postings as a discovery source.
func LeverSite(site, company string) store.Source {
	return store.Source{ID: "lever:" + site, Kind: KindLever, Name: company + " (Lever)", Enabled: true,
		Config: map[string]string{"site": site, "base_url": "https://api.lever.co"},
		Policy: domain.SourcePolicy{TermsURL: "https://github.com/lever/postings-api", TermsVerified: true, TermsVerifiedOn: "2026-11-20",
			RateLimitPerMinute: 30, RetentionDays: 365, Capabilities: []domain.CapabilityGrant{
				grant(domain.CapDiscoverJobs, domain.MethodPublicAPI, true, "Postings API: published postings are publicly viewable and 'may be scraped by third parties'."),
				grant(domain.CapReadJobDetails, domain.MethodPublicAPI, true, "Same public postings API."),
				grant(domain.CapSubmitApplication, domain.MethodAuthorizedAPI, false, "POST apply needs an API key generated by the employer's Lever Super Admin."),
				grant(domain.CapSubmitApplication, domain.MethodHandoff, true, basisHandoff),
			}}}
}

// MockATS is the test-only source: a local Greenhouse-shaped board that also accepts
// applications with a key the candidate holds, so the automated path can be exercised
// end to end without touching a real employer.
func MockATS(baseURL, boardToken string) store.Source {
	return store.Source{ID: "mockats:" + boardToken, Kind: KindMockATS, Name: "Mock ATS (" + boardToken + ")", Enabled: true,
		Config: map[string]string{"board_token": boardToken, "base_url": baseURL},
		Policy: domain.SourcePolicy{TermsURL: "local test fixture", TermsVerified: true, AutomatedSubmit: true, RateLimitPerMinute: 60,
			DailySubmitLimit: 20, RetentionDays: 30, Capabilities: []domain.CapabilityGrant{
				grant(domain.CapDiscoverJobs, domain.MethodPublicAPI, true, "Local mock ATS."),
				grant(domain.CapReadJobDetails, domain.MethodPublicAPI, true, "Local mock ATS."),
				grant(domain.CapSubmitApplication, domain.MethodAuthorizedAPI, true, "Local mock ATS; key held by the candidate."),
				grant(domain.CapTrackApplication, domain.MethodAuthorizedAPI, true, "Local mock ATS lookup endpoint."),
			}}}
}
