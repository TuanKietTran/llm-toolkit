package sources

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/TuanKietTran/llm-toolkit/packages/job-cv-finder/core/internal/domain"
)

func TestGreenhouseDiscoverReadsJobsAndQuestions(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "" {
			t.Error("discovery must not send credentials")
		}
		switch r.URL.Path {
		case "/v1/boards/acme/jobs":
			if r.URL.Query().Get("content") != "true" {
				t.Error("content=true missing")
			}
			w.Write([]byte(`{"jobs":[{"id":42,"title":"Backend Engineer (Go)","absolute_url":"https://boards.greenhouse.io/acme/jobs/42","updated_at":"2026-11-01T00:00:00Z","location":{"name":"Ho Chi Minh City"},"content":"&lt;p&gt;Requirements: Golang&lt;/p&gt;"}]}`))
		case "/v1/boards/acme/jobs/42":
			w.Write([]byte(`{"id":42,"title":"Backend Engineer (Go)","questions":[{"label":"Resume","required":true,"fields":[{"name":"resume","type":"input_file"}]},{"label":"Notice period","required":true,"fields":[{"name":"question_1","type":"input_text"}]}]}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	src := GreenhouseBoard("acme", "Acme")
	src.Config["base_url"] = srv.URL
	reg := NewRegistry(srv.Client())
	d, err := reg.Discoverer(src)
	if err != nil {
		t.Fatal(err)
	}
	jobs, err := d.Discover(context.Background(), src)
	if err != nil || len(jobs) != 1 {
		t.Fatalf("%v %d", err, len(jobs))
	}
	j := jobs[0]
	if j.ExternalJobID != "42" || j.Company != "Acme" || len(j.Questions) != 2 || j.Questions[0].Type != "file" || !j.Questions[1].Required {
		t.Fatalf("%+v", j)
	}
}

func TestLeverDiscover(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v0/postings/acme" || r.URL.Query().Get("mode") != "json" {
			http.NotFound(w, r)
			return
		}
		w.Write([]byte(`[{"id":"abc-123","text":"Platform Engineer","hostedUrl":"https://jobs.lever.co/acme/abc-123","applyUrl":"https://jobs.lever.co/acme/abc-123/apply","createdAt":1762000000000,"categories":{"location":"Ho Chi Minh City","commitment":"Full-time"},"workplaceType":"hybrid","descriptionPlain":"Build things.","lists":[{"text":"Requirements","content":"<li>Kubernetes</li>"}],"additionalPlain":"","salaryRange":{"min":2000,"max":3000,"currency":"USD"}}]`))
	}))
	defer srv.Close()
	src := LeverSite("acme", "Acme")
	src.Config["base_url"] = srv.URL
	jobs, err := (&Lever{Client: srv.Client()}).Discover(context.Background(), src)
	if err != nil || len(jobs) != 1 {
		t.Fatalf("%v %d", err, len(jobs))
	}
	if jobs[0].SalaryText != "2000 - 3000 USD" || jobs[0].ApplicationURL != "https://jobs.lever.co/acme/abc-123/apply" {
		t.Fatalf("%+v", jobs[0])
	}
}

func TestRegistryRefusesNonPollingSources(t *testing.T) {
	reg := NewRegistry(nil)
	for _, src := range Defaults() {
		if _, err := reg.Discoverer(src); err == nil {
			t.Errorf("%s: platform sources must not be polled", src.ID)
		}
	}
}

func TestDefaultPoliciesNeverPermitAutomatedPlatformSubmission(t *testing.T) {
	for _, src := range Defaults() {
		if m, ok := src.Policy.Permits(domain.CapSubmitApplication); !ok || m != domain.MethodHandoff {
			t.Errorf("%s: submit must be handoff, got %s %v", src.ID, m, ok)
		}
		for _, g := range src.Policy.Capabilities {
			if g.Method == domain.MethodBrowser && g.Permitted {
				t.Errorf("%s: browser automation permitted by default", src.ID)
			}
		}
	}
	gh := GreenhouseBoard("x", "X")
	if m, _ := gh.Policy.Permits(domain.CapSubmitApplication); m != domain.MethodHandoff {
		t.Error("greenhouse submit must be handoff for a candidate")
	}
}

func TestGreenhouseLocationFilterAndPacing(t *testing.T) {
	details := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/boards/acme/jobs":
			w.Write([]byte(`{"jobs":[{"id":1,"title":"A","location":{"name":"Remote, Singapore"}},{"id":2,"title":"B","location":{"name":"Berlin"}},{"id":3,"title":"C","location":{"name":"Ho Chi Minh City"}}]}`))
		default:
			details++
			w.Write([]byte(`{"questions":[]}`))
		}
	}))
	defer srv.Close()
	src := GreenhouseBoard("acme", "Acme")
	src.Config["base_url"] = srv.URL
	src.Config["location_filter"] = "singapore|ho chi minh"
	src.Policy.RateLimitPerMinute = 600 // 100ms between detail calls
	start := time.Now()
	jobs, err := (&Greenhouse{Client: srv.Client()}).Discover(context.Background(), src)
	if err != nil {
		t.Fatal(err)
	}
	if len(jobs) != 2 || details != 2 {
		t.Fatalf("filter: %d jobs, %d detail calls", len(jobs), details)
	}
	if time.Since(start) < 100*time.Millisecond {
		t.Fatal("detail calls were not paced")
	}
	src.Config["location_filter"] = "("
	if _, err := (&Greenhouse{Client: srv.Client()}).Discover(context.Background(), src); err == nil {
		t.Fatal("bad filter accepted")
	}
}
