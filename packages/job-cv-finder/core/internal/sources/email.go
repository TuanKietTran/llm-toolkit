package sources

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"mime/quotedprintable"
	"net/mail"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/TuanKietTran/llm-toolkit/packages/job-cv-finder/core/internal/domain"
	"github.com/TuanKietTran/llm-toolkit/packages/job-cv-finder/core/internal/normalize"
)

// Email is the subset of a message the parsers need. Bodies are untrusted data: parsers
// extract fields with fixed patterns and never interpret content as instructions.
type Email struct {
	MessageID string    `json:"message_id"`
	From      string    `json:"from"`
	Subject   string    `json:"subject"`
	Date      time.Time `json:"date"`
	Text      string    `json:"text"`
	HTML      string    `json:"html,omitempty"`
}

// Classification of an inbound email.
const (
	ClassLinkedInAlert        = "linkedin_alert"
	ClassLinkedInConfirmation = "linkedin_confirmation"
	ClassLinkedInViewed       = "linkedin_viewed"
	ClassITviecAlert          = "itviec_alert"
	ClassITviecConfirmation   = "itviec_confirmation"
	ClassUnknown              = "unknown"
)

// Confirmation is evidence, from the platform itself, that an application was submitted.
type Confirmation struct {
	SourceID      string `json:"source_id"`
	ExternalJobID string `json:"external_job_id,omitempty"` // LinkedIn includes it; ITviec does not
	Company       string `json:"company"`
	Title         string `json:"title"`
	AppliedOn     string `json:"applied_on,omitempty"`
	MessageID     string `json:"message_id"`
	Subject       string `json:"subject"`
}

// ParsedEmail is the parser output.
type ParsedEmail struct {
	Classification string          `json:"classification"`
	SourceID       string          `json:"source_id,omitempty"`
	Jobs           []domain.RawJob `json:"-"`
	JobCount       int             `json:"job_count"`
	Confirmation   *Confirmation   `json:"confirmation,omitempty"`
	ViewedBy       string          `json:"viewed_by,omitempty"`
}

var (
	reLinkedInJobID  = regexp.MustCompile(`linkedin\.com/(?:comm/)?jobs/view/(\d{6,})`)
	reSeparator      = regexp.MustCompile(`(?m)^-{10,}\s*$`)
	reITviecConfirm1 = regexp.MustCompile(`^Chúc mừng! Hồ sơ của bạn cho vị trí (.+?) (?:tại|đến) (.+?) đã được gửi đi!?$`)
	reITviecConfirm2 = regexp.MustCompile(`^ITviec đã nhận hồ sơ của bạn cho vị trí (.+?) tại (.+?)$`)
	reITviecJob      = regexp.MustCompile(`(?m)^\s*Job \d+:\s*(.+)$`)
	reAppliedOn      = regexp.MustCompile(`Applied on ([A-Z][a-z]+ \d{1,2}, \d{4})`)
	reViewedBy       = regexp.MustCompile(`(?i)your application was viewed by (.+)$`)
	reSentTo         = regexp.MustCompile(`(?i)your application was sent to (.+)$`)
)

var linkedInNoise = []*regexp.Regexp{
	regexp.MustCompile(`(?i)^your job alert for`),
	regexp.MustCompile(`(?i)^your application was sent to`),
	regexp.MustCompile(`(?i)^apply with resume`),
	regexp.MustCompile(`(?i)^easy apply$`),
	regexp.MustCompile(`(?i)^\d+ (connections?|school alum(ni)?|company alum(ni)?)$`),
	regexp.MustCompile(`(?i)^this company is actively hiring$`),
	regexp.MustCompile(`(?i)^actively recruiting$`),
	regexp.MustCompile(`(?i)^promoted$`),
	regexp.MustCompile(`(?i)^(be an early applicant|\d+ applicants?)$`),
	regexp.MustCompile(`(?i)^view job:`),
}

func senderAddress(from string) string {
	if a, err := mail.ParseAddress(from); err == nil {
		return strings.ToLower(a.Address)
	}
	return strings.ToLower(strings.Trim(from, "<> "))
}

// Parse classifies an email and extracts jobs or a confirmation.
func Parse(e Email) ParsedEmail {
	from := senderAddress(e.From)
	subject := strings.TrimSpace(e.Subject)
	switch {
	case strings.HasSuffix(from, "@linkedin.com"):
		if m := reSentTo.FindStringSubmatch(subject); m != nil {
			return parseLinkedInConfirmation(e, strings.TrimSpace(m[1]))
		}
		if m := reViewedBy.FindStringSubmatch(subject); m != nil {
			return ParsedEmail{Classification: ClassLinkedInViewed, SourceID: KindLinkedIn, ViewedBy: strings.TrimSpace(m[1])}
		}
		jobs := parseLinkedInBlocks(e.Text, e.Date)
		if len(jobs) > 0 {
			return ParsedEmail{Classification: ClassLinkedInAlert, SourceID: KindLinkedIn, Jobs: jobs, JobCount: len(jobs)}
		}
	case strings.HasSuffix(from, "itviec.com"):
		if m := reITviecConfirm1.FindStringSubmatch(subject); m != nil {
			return itviecConfirmation(e, m[1], m[2])
		}
		if m := reITviecConfirm2.FindStringSubmatch(subject); m != nil {
			return itviecConfirmation(e, m[1], m[2])
		}
		if jobs := parseITviecAlert(e.Text, e.Date); len(jobs) > 0 {
			return ParsedEmail{Classification: ClassITviecAlert, SourceID: KindITviec, Jobs: jobs, JobCount: len(jobs)}
		}
	}
	return ParsedEmail{Classification: ClassUnknown}
}

func parseLinkedInConfirmation(e Email, company string) ParsedEmail {
	// Only the FIRST block is the confirmed job; later blocks are "similar jobs" suggestions.
	blocks := reSeparator.Split(e.Text, 2)
	c := &Confirmation{SourceID: KindLinkedIn, Company: company, MessageID: e.MessageID, Subject: e.Subject}
	if len(blocks) > 0 {
		if j, ok := linkedInBlock(blocks[0], e.Date); ok {
			c.ExternalJobID, c.Title = j.ExternalJobID, j.Title
			if j.Company != "" {
				c.Company = j.Company
			}
		}
	}
	if m := reAppliedOn.FindStringSubmatch(e.Text); m != nil {
		c.AppliedOn = m[1]
	}
	return ParsedEmail{Classification: ClassLinkedInConfirmation, SourceID: KindLinkedIn, Confirmation: c}
}

func itviecConfirmation(e Email, title, company string) ParsedEmail {
	return ParsedEmail{Classification: ClassITviecConfirmation, SourceID: KindITviec, Confirmation: &Confirmation{
		SourceID: KindITviec, Title: strings.TrimSpace(title), Company: strings.TrimSpace(company), MessageID: e.MessageID, Subject: e.Subject,
		AppliedOn: e.Date.UTC().Format("2006-01-02")}}
}

func parseLinkedInBlocks(text string, date time.Time) []domain.RawJob {
	var out []domain.RawJob
	for _, b := range reSeparator.Split(text, -1) {
		if j, ok := linkedInBlock(b, date); ok {
			out = append(out, j)
		}
	}
	return out
}

func linkedInBlock(block string, date time.Time) (domain.RawJob, bool) {
	m := reLinkedInJobID.FindStringSubmatch(block)
	if m == nil {
		return domain.RawJob{}, false
	}
	var lines []string
	for _, l := range strings.Split(block, "\n") {
		l = strings.TrimSpace(l)
		if l == "" {
			continue
		}
		if strings.HasPrefix(strings.ToLower(l), "view job:") {
			break
		}
		noise := false
		for _, re := range linkedInNoise {
			if re.MatchString(l) {
				noise = true
				break
			}
		}
		if !noise {
			lines = append(lines, l)
		}
	}
	if len(lines) < 2 {
		return domain.RawJob{}, false
	}
	j := domain.RawJob{ExternalJobID: m[1], Title: lines[0], Company: lines[1],
		ApplicationURL: "https://www.linkedin.com/jobs/view/" + m[1] + "/", Partial: true,
		RawContentType: "text/plain", Raw: []byte(strings.TrimSpace(block))}
	if len(lines) > 2 {
		j.Location = lines[2]
	}
	d := date
	j.PublishedAt = &d
	return j, true
}

func parseITviecAlert(text string, date time.Time) []domain.RawJob {
	idx := reITviecJob.FindAllStringSubmatchIndex(text, -1)
	var out []domain.RawJob
	for i, loc := range idx {
		end := len(text)
		if i+1 < len(idx) {
			end = idx[i+1][0]
		}
		chunk := text[loc[0]:end]
		if k := strings.Index(chunk, "\n\n"); k > 0 {
			chunk = chunk[:k]
		}
		title := strings.TrimSpace(text[loc[2]:loc[3]])
		field := func(name string) string {
			re := regexp.MustCompile(`(?m)^\s*` + name + `:\s*(.+)$`)
			if m := re.FindStringSubmatch(chunk); m != nil {
				return strings.TrimSpace(m[1])
			}
			return ""
		}
		company := field("Employer")
		if title == "" || company == "" {
			continue
		}
		var skills []string
		for _, s := range strings.Split(field("Required Skills"), ",") {
			if s = strings.TrimSpace(s); s != "" {
				skills = append(skills, s)
			}
		}
		// ITviec alerts carry only tracking redirects, so the id is derived from the content
		// and the handoff URL is an ITviec search the candidate opens themselves.
		sum := sha256.Sum256([]byte(normalize.CanonicalKey(company, title)))
		d := date
		out = append(out, domain.RawJob{ExternalJobID: "itv-" + hex.EncodeToString(sum[:6]), Title: title, Company: company,
			SalaryText: field("Salary"), Skills: skills, Partial: true, PublishedAt: &d,
			ApplicationURL: "https://itviec.com/it-jobs?query=" + url.QueryEscape(title+" "+company),
			RawContentType: "text/plain", Raw: []byte(strings.TrimSpace(chunk))})
	}
	return out
}

// ParseRFC822 reads a raw message (as exported from a mail client) into an Email, preferring
// the text/plain part and falling back to stripped HTML.
func ParseRFC822(raw []byte) (Email, error) {
	msg, err := mail.ReadMessage(bytes.NewReader(raw))
	if err != nil {
		return Email{}, fmt.Errorf("read message: %w", err)
	}
	dec := new(mime.WordDecoder)
	subject, err := dec.DecodeHeader(msg.Header.Get("Subject"))
	if err != nil {
		subject = msg.Header.Get("Subject")
	}
	from, _ := dec.DecodeHeader(msg.Header.Get("From"))
	e := Email{MessageID: strings.Trim(msg.Header.Get("Message-Id"), "<> "), From: from, Subject: subject}
	if d, err := msg.Header.Date(); err == nil {
		e.Date = d.UTC()
	}
	if e.MessageID == "" {
		sum := sha256.Sum256(raw)
		e.MessageID = "sha256:" + hex.EncodeToString(sum[:])
	}
	text, html, err := bodies(msg.Header.Get("Content-Type"), msg.Header.Get("Content-Transfer-Encoding"), msg.Body, 0)
	if err != nil {
		return e, err
	}
	e.Text, e.HTML = text, html
	if e.Text == "" && html != "" {
		e.Text = normalize.StripHTML(html)
	}
	return e, nil
}

func bodies(ctype, cte string, r io.Reader, depth int) (text, html string, err error) {
	if depth > 5 {
		return "", "", nil
	}
	mt, params, perr := mime.ParseMediaType(ctype)
	if perr != nil {
		mt = "text/plain"
	}
	if strings.HasPrefix(mt, "multipart/") {
		mr := multipart.NewReader(r, params["boundary"])
		for {
			p, err := mr.NextPart()
			if err == io.EOF {
				return text, html, nil
			}
			if err != nil {
				return text, html, err
			}
			t, h, err := bodies(p.Header.Get("Content-Type"), p.Header.Get("Content-Transfer-Encoding"), p, depth+1)
			if err != nil {
				return text, html, err
			}
			if text == "" {
				text = t
			}
			if html == "" {
				html = h
			}
		}
	}
	var body io.Reader = io.LimitReader(r, maxBody)
	switch strings.ToLower(cte) {
	case "quoted-printable":
		body = quotedprintable.NewReader(body)
	case "base64":
		body = newBase64Reader(body)
	}
	b, err := io.ReadAll(body)
	if err != nil {
		return "", "", err
	}
	switch mt {
	case "text/plain":
		return string(b), "", nil
	case "text/html":
		return "", string(b), nil
	}
	return "", "", nil
}
