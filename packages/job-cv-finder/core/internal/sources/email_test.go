package sources

import (
	"strings"
	"testing"
	"time"
)

// Fixtures reproduce the LAYOUT of real platform emails (Nov 2026) with synthetic people,
// companies and tracking parameters.

const linkedInAlert = `Your job alert for Backend Engineer (Python/Go), on-site or hybrid or remote in Ho Chi Minh City
Generative AI Engineer
Example Software
Ho Chi Minh City, Vietnam

1 connection
Apply with resume & profile
View job: https://www.linkedin.com/comm/jobs/view/4490000001/?trackingId=abc&refId=def

---------------------------------------------------------

Data Engineer (Machine Learning & Data Platform)
Sample Labs
Ho Chi Minh City, Vietnam

5 school alumni
Apply with resume & profile
View job: https://www.linkedin.com/comm/jobs/view/4490000002/?trackingId=abc

---------------------------------------------------------

XNO - AI Engineer - HCM
Demo Capital Technology
District 2
Apply with resume & profile
View job: https://www.linkedin.com/comm/jobs/view/4490000003/?trackingId=abc

---------------------------------------------------------

See all jobs on LinkedIn: https://www.linkedin.com/comm/jobs/search-results/?x=1
Manage your job alerts: https://www.linkedin.com/comm/jobs/alerts?x=1
`

const linkedInConfirm = `Your application was sent to Demo Tech Vietnam

Software Engineer (Middle to Senior)
Demo Tech Vietnam
Ho Chi Minh City, Vietnam
View job: https://www.linkedin.com/comm/jobs/view/4486000000/?trackingId=xyz

---------------------------------------------------------

Applied on November 10, 2026----------------------------------------------------------------
        Now, take these next steps for more success

View similar jobs you may be interested in

Software Engineer
Other Corp
Vietnam

4 school alumni
View job: https://www.linkedin.com/comm/jobs/view/4383000000/?trackingId=q
`

const itviecAlert = `          Hi Candidate,

          This job may be your next one. Apply today!
          View My Jobs (https:/itviec.com/it-jobs/typescript-reactjs/ho-chi-minh-hcm?x=1)

          **************************************************

          Job 1: Technical leader (cloud experience)
          Employer: Example Link
          Salary: 1,500 - 2,500 USD
          Required Skills: Leadership, CI/CD, Python, TypeScript, JavaScript, AWS

          Job 2: Senior Backend Engineer (Go)
          Employer: Sample Pay
          Salary: You'll love it
          Required Skills: Golang, PostgreSQL

          More TypeScript jobs in Ho Chi Minh https:/itviec.com/it-jobs/typescript
`

var when = time.Date(2026, 11, 16, 11, 42, 0, 0, time.UTC)

func TestLinkedInAlert(t *testing.T) {
	p := Parse(Email{MessageID: "m1", From: "LinkedIn Job Alerts <jobalerts-noreply@linkedin.com>", Subject: "Example Software is hiring a Generative AI Engineer", Date: when, Text: linkedInAlert})
	if p.Classification != ClassLinkedInAlert || len(p.Jobs) != 3 {
		t.Fatalf("got %s with %d jobs", p.Classification, len(p.Jobs))
	}
	j := p.Jobs[0]
	if j.ExternalJobID != "4490000001" || j.Title != "Generative AI Engineer" || j.Company != "Example Software" || j.Location != "Ho Chi Minh City, Vietnam" {
		t.Fatalf("job 0: %+v", j)
	}
	if !j.Partial || j.ApplicationURL != "https://www.linkedin.com/jobs/view/4490000001/" {
		t.Fatalf("partial/url wrong: %+v", j)
	}
	if strings.Contains(j.ApplicationURL, "tracking") {
		t.Fatal("tracking parameters must be dropped")
	}
	if p.Jobs[2].Location != "District 2" || p.Jobs[2].Company != "Demo Capital Technology" {
		t.Fatalf("job 2: %+v", p.Jobs[2])
	}
}

func TestLinkedInConfirmationTakesOnlyFirstBlock(t *testing.T) {
	p := Parse(Email{MessageID: "m2", From: "jobs-noreply@linkedin.com", Subject: "Candidate, your application was sent to Demo Tech Vietnam", Date: when, Text: linkedInConfirm})
	if p.Classification != ClassLinkedInConfirmation || p.Confirmation == nil {
		t.Fatalf("got %+v", p)
	}
	c := p.Confirmation
	if c.ExternalJobID != "4486000000" || c.Title != "Software Engineer (Middle to Senior)" || c.Company != "Demo Tech Vietnam" || c.AppliedOn != "November 10, 2026" {
		t.Fatalf("confirmation: %+v", c)
	}
	if len(p.Jobs) != 0 {
		t.Fatal("similar-job suggestions must not be treated as applications or alerts")
	}
}

func TestLinkedInViewed(t *testing.T) {
	p := Parse(Email{From: "jobs-noreply@linkedin.com", Subject: "Your application was viewed by Career Example"})
	if p.Classification != ClassLinkedInViewed || p.ViewedBy != "Career Example" {
		t.Fatalf("%+v", p)
	}
}

func TestITviecAlertAndConfirmations(t *testing.T) {
	p := Parse(Email{MessageID: "m3", From: "ITviec <itviec+ijm+1@itviec.com>", Subject: "Candidate, looking for a TypeScript job? Example Link has opening.", Date: when, Text: itviecAlert})
	if p.Classification != ClassITviecAlert || len(p.Jobs) != 2 {
		t.Fatalf("got %s %d", p.Classification, len(p.Jobs))
	}
	j := p.Jobs[0]
	if j.Title != "Technical leader (cloud experience)" || j.Company != "Example Link" || j.SalaryText != "1,500 - 2,500 USD" || len(j.Skills) != 6 {
		t.Fatalf("job: %+v", j)
	}
	if !strings.HasPrefix(j.ExternalJobID, "itv-") || p.Jobs[1].ExternalJobID == j.ExternalJobID {
		t.Fatal("derived ids must be stable and distinct")
	}
	again := Parse(Email{From: "itviec@itviec.com", Subject: "x", Date: when, Text: itviecAlert})
	if again.Jobs[0].ExternalJobID != j.ExternalJobID {
		t.Fatal("derived id not deterministic")
	}

	for _, subj := range []string{
		"Chúc mừng! Hồ sơ của bạn cho vị trí Backend Engineer (Python, SQL, English) tại Example – House Of Norway đã được gửi đi!",
		"ITviec đã nhận hồ sơ của bạn cho vị trí Backend Engineer (Python, SQL, English) tại Example – House Of Norway",
	} {
		c := Parse(Email{MessageID: "m", From: "itviec@itviec.com", Subject: subj, Date: when})
		if c.Classification != ClassITviecConfirmation || c.Confirmation.Title != "Backend Engineer (Python, SQL, English)" || c.Confirmation.Company != "Example – House Of Norway" {
			t.Fatalf("%q -> %+v", subj, c.Confirmation)
		}
	}
}

func TestMarketingAndForeignSendersAreUnknown(t *testing.T) {
	for _, e := range []Email{
		{From: "itviec@itviec.com", Subject: "Cơ hội nghề nghiệp: Tham dự Career Day 2026 miễn phí!", Text: "event"},
		{From: "attacker@example.com", Subject: "Your application was sent to Acme", Text: "View job: https://www.linkedin.com/jobs/view/1234567/"},
	} {
		if p := Parse(e); p.Classification != ClassUnknown {
			t.Fatalf("%q classified as %s", e.Subject, p.Classification)
		}
	}
}

func TestParseRFC822Multipart(t *testing.T) {
	raw := "From: LinkedIn <jobs-noreply@linkedin.com>\r\n" +
		"Subject: =?UTF-8?B?" + "Q2FuZGlkYXRlLCB5b3VyIGFwcGxpY2F0aW9uIHdhcyBzZW50IHRvIERlbW8gVGVjaCBWaWV0bmFt" + "?=\r\n" +
		"Message-ID: <abc@linkedin.com>\r\nDate: Tue, 10 Nov 2026 09:21:25 +0000\r\nMIME-Version: 1.0\r\n" +
		"Content-Type: multipart/alternative; boundary=XX\r\n\r\n--XX\r\nContent-Type: text/plain; charset=utf-8\r\nContent-Transfer-Encoding: quoted-printable\r\n\r\n" +
		strings.ReplaceAll(linkedInConfirm, "=", "=3D") + "\r\n--XX\r\nContent-Type: text/html\r\n\r\n<p>x</p>\r\n--XX--\r\n"
	e, err := ParseRFC822([]byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	if e.MessageID != "abc@linkedin.com" || !strings.Contains(e.Subject, "your application was sent to Demo Tech Vietnam") {
		t.Fatalf("headers: %+v", e)
	}
	p := Parse(e)
	if p.Classification != ClassLinkedInConfirmation || p.Confirmation.ExternalJobID != "4486000000" {
		t.Fatalf("parsed: %+v", p.Confirmation)
	}
}
