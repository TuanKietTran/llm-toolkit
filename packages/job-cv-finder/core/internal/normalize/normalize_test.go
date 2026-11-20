package normalize

import (
	"slices"
	"testing"
	"time"

	"github.com/TuanKietTran/llm-toolkit/packages/job-cv-finder/core/internal/domain"
)

func TestCompanyKeyCollapsesLegalAndCountrySuffixes(t *testing.T) {
	cases := map[string]string{
		"giftee Tech Vietnam":       "giftee tech",
		"Giftee Tech":               "giftee tech",
		"FPT Software Co., Ltd":     "fpt software",
		"Restaff – House Of Norway": "restaff house of norway",
		"Công ty TNHH Đất Việt JSC": "cong ty tnhh dat viet",
		"Hubble Pte. Ltd":           "hubble",
	}
	for in, want := range cases {
		if got := Company(in); got != want {
			t.Errorf("Company(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestTitleKeyKeepsSeniorityAndStack(t *testing.T) {
	a := Title("Backend Engineer (Python, SQL, English)")
	if a != "backend engineer python sql english" {
		t.Fatalf("got %q", a)
	}
	if Title("Senior Go Engineer") == Title("Junior Go Engineer") {
		t.Fatal("seniority must distinguish titles")
	}
}

func TestLocationAndWorkMode(t *testing.T) {
	for _, in := range []string{"Ho Chi Minh City, Vietnam", "Hồ Chí Minh", "District 2", "HCM"} {
		if Location(in) != "Ho Chi Minh City" {
			t.Errorf("Location(%q) = %q", in, Location(in))
		}
	}
	if WorkMode("", "Remote-Fullstack Developer", "") != "remote" {
		t.Error("remote title not detected")
	}
	if WorkMode("Ho Chi Minh City", "Engineer", "We work in a hybrid model") != "hybrid" {
		t.Error("hybrid not detected")
	}
	if WorkMode("Ho Chi Minh City", "Engineer", "") != "unknown" {
		t.Error("must stay unknown when nothing is stated")
	}
}

func TestSalary(t *testing.T) {
	lo, hi, cur := Salary("1,500 - 2,500 USD")
	if lo == nil || hi == nil || *lo != 1500 || *hi != 2500 || cur != "USD" {
		t.Fatalf("got %v %v %s", lo, hi, cur)
	}
	lo, hi, cur = Salary("Up to 3000 USD")
	if lo != nil || hi == nil || *hi != 3000 || cur != "USD" {
		t.Fatalf("up to: got %v %v %s", lo, hi, cur)
	}
	lo, hi, _ = Salary("20 - 30 triệu")
	if lo == nil || *lo != 20e6 || *hi != 30e6 {
		t.Fatalf("triệu: got %v %v", lo, hi)
	}
	if lo, hi, cur := Salary("You'll love it"); lo != nil || hi != nil || cur != "" {
		t.Fatal("unreadable salary must stay unknown")
	}
	if lo, _, _ := Salary("2000"); lo != nil {
		t.Fatal("bare number without currency must stay unknown")
	}
}

func TestSkillsSplitRequiredPreferred(t *testing.T) {
	desc := `Requirements
- 3+ years with Golang and PostgreSQL
- Docker
Nice to have
- Kubernetes, Kafka
- Python`
	req, pref := Skills(desc, nil)
	for _, s := range []string{"Go", "PostgreSQL", "Docker"} {
		if !slices.Contains(req, s) {
			t.Errorf("required missing %s: %v", s, req)
		}
	}
	for _, s := range []string{"Kubernetes", "Kafka", "Python"} {
		if !slices.Contains(pref, s) {
			t.Errorf("preferred missing %s: %v", s, pref)
		}
	}
	if Experience(desc) != "3+ years" {
		t.Errorf("experience %q", Experience(desc))
	}
}

func TestSkillsDoNotMatchInsideWords(t *testing.T) {
	req, _ := Skills("We use javascript and cargo/ships; Django is fine.", nil)
	if slices.Contains(req, "Java") || slices.Contains(req, "Go") {
		t.Fatalf("false positive: %v", req)
	}
}

func TestNormalizeAndDedupKey(t *testing.T) {
	now := time.Date(2026, 11, 20, 0, 0, 0, 0, time.UTC)
	a := Normalize("linkedin", domain.RawJob{ExternalJobID: "1", Company: "giftee Tech Vietnam", Title: "Software Engineer (Middle to Senior)", Location: "Ho Chi Minh City, Vietnam", Partial: true}, now)
	b := Normalize("itviec", domain.RawJob{ExternalJobID: "x", Company: "Giftee Tech", Title: "Software Engineer - Middle to Senior", Location: "HCM", Description: "<p>Go &amp; Docker</p>"}, now)
	if a.CanonicalKey != b.CanonicalKey {
		t.Fatalf("keys differ: %q vs %q", a.CanonicalKey, b.CanonicalKey)
	}
	if a.Completeness != "partial" || b.Completeness != "full" {
		t.Fatal("completeness wrong")
	}
	if b.Description != "Go & Docker" {
		t.Fatalf("html not stripped: %q", b.Description)
	}
	if a.ContentHash == b.ContentHash {
		t.Fatal("different content must hash differently")
	}
}

func TestSimilarIsSecondaryAndCompanyScoped(t *testing.T) {
	now := time.Now()
	a := Normalize("s", domain.RawJob{Company: "Acme", Title: "Senior Backend Engineer Go"}, now)
	b := Normalize("s", domain.RawJob{Company: "Acme", Title: "Senior Backend Engineer (Go)"}, now)
	c := Normalize("s", domain.RawJob{Company: "Other", Title: "Senior Backend Engineer Go"}, now)
	if !Similar(a, b) || Similar(a, c) {
		t.Fatal("similarity wrong")
	}
}
