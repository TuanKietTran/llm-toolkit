package sources

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/TuanKietTran/llm-toolkit/packages/job-cv-finder/core/internal/domain"
	"github.com/TuanKietTran/llm-toolkit/packages/job-cv-finder/core/internal/store"
)

const maxBody = 20 << 20

func getJSON(ctx context.Context, c *http.Client, u string, into any) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "job-cv-finder/0.1 (personal job search)")
	resp, err := c.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBody))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode == http.StatusTooManyRequests {
		return nil, fmt.Errorf("rate limited (429) by %s", req.URL.Host)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GET %s: HTTP %d", req.URL.Redacted(), resp.StatusCode)
	}
	return body, json.Unmarshal(body, into)
}

// Greenhouse reads a public Job Board (GET only; no credentials).
type Greenhouse struct{ Client *http.Client }

type ghJobs struct {
	Jobs []json.RawMessage `json:"jobs"`
}

type ghJob struct {
	ID          int64  `json:"id"`
	Title       string `json:"title"`
	AbsoluteURL string `json:"absolute_url"`
	UpdatedAt   string `json:"updated_at"`
	Content     string `json:"content"`
	CompanyName string `json:"company_name"`
	Location    struct {
		Name string `json:"name"`
	} `json:"location"`
	Questions []struct {
		Label    string `json:"label"`
		Required bool   `json:"required"`
		Fields   []struct {
			Name string `json:"name"`
			Type string `json:"type"`
		} `json:"fields"`
	} `json:"questions"`
	Metadata []struct {
		Name  string `json:"name"`
		Value any    `json:"value"`
	} `json:"metadata"`
}

// Discover lists jobs with content, then reads each job's questions.
func (g *Greenhouse) Discover(ctx context.Context, src store.Source) ([]domain.RawJob, error) {
	base := strings.TrimRight(src.Config["base_url"], "/")
	token := src.Config["board_token"]
	if base == "" || token == "" {
		return nil, fmt.Errorf("greenhouse source %s: base_url and board_token required", src.ID)
	}
	company := src.Config["company"]
	if company == "" {
		company = strings.TrimSuffix(strings.TrimSuffix(src.Name, " (Greenhouse)"), " (Mock ATS)")
	}
	var list ghJobs
	if _, err := getJSON(ctx, g.Client, fmt.Sprintf("%s/v1/boards/%s/jobs?content=true", base, url.PathEscape(token)), &list); err != nil {
		return nil, err
	}
	out := make([]domain.RawJob, 0, len(list.Jobs))
	for _, raw := range list.Jobs {
		var j ghJob
		if err := json.Unmarshal(raw, &j); err != nil {
			return nil, fmt.Errorf("decode job: %w", err)
		}
		// Questions come from the per-job endpoint; failure there must not drop the job.
		var detail ghJob
		detailRaw, err := getJSON(ctx, g.Client, fmt.Sprintf("%s/v1/boards/%s/jobs/%d?questions=true", base, url.PathEscape(token), j.ID), &detail)
		if err == nil {
			j.Questions = detail.Questions
			raw = detailRaw
		}
		var qs []domain.Question
		for _, q := range j.Questions {
			for _, f := range q.Fields {
				t := "text"
				switch f.Type {
				case "input_file":
					t = "file"
				case "textarea":
					t = "textarea"
				case "multi_value_single_select":
					t = "select"
				}
				qs = append(qs, domain.Question{ID: f.Name, Label: q.Label, Required: q.Required, Type: t})
			}
		}
		var pub *time.Time
		if t, err := time.Parse(time.RFC3339, j.UpdatedAt); err == nil {
			pub = &t
		}
		name := j.CompanyName
		if name == "" {
			name = company
		}
		out = append(out, domain.RawJob{ExternalJobID: strconv.FormatInt(j.ID, 10), Company: name, Title: j.Title, Description: j.Content,
			Location: j.Location.Name, ApplicationURL: j.AbsoluteURL, PublishedAt: pub, Questions: qs,
			RawContentType: "application/json", Raw: raw})
	}
	return out, nil
}

// Lever reads public postings (GET only; no credentials).
type Lever struct{ Client *http.Client }

type leverPosting struct {
	ID         string `json:"id"`
	Text       string `json:"text"`
	HostedURL  string `json:"hostedUrl"`
	ApplyURL   string `json:"applyUrl"`
	CreatedAt  int64  `json:"createdAt"`
	Categories struct {
		Location   string `json:"location"`
		Commitment string `json:"commitment"`
	} `json:"categories"`
	WorkplaceType    string `json:"workplaceType"`
	DescriptionPlain string `json:"descriptionPlain"`
	AdditionalPlain  string `json:"additionalPlain"`
	Lists            []struct {
		Text    string `json:"text"`
		Content string `json:"content"`
	} `json:"lists"`
	SalaryRange *struct {
		Min      float64 `json:"min"`
		Max      float64 `json:"max"`
		Currency string  `json:"currency"`
	} `json:"salaryRange"`
}

// Discover lists published postings for one Lever site.
func (l *Lever) Discover(ctx context.Context, src store.Source) ([]domain.RawJob, error) {
	base := strings.TrimRight(src.Config["base_url"], "/")
	site := src.Config["site"]
	if base == "" || site == "" {
		return nil, fmt.Errorf("lever source %s: base_url and site required", src.ID)
	}
	var raws []json.RawMessage
	if _, err := getJSON(ctx, l.Client, fmt.Sprintf("%s/v0/postings/%s?mode=json", base, url.PathEscape(site)), &raws); err != nil {
		return nil, err
	}
	company := strings.TrimSuffix(src.Name, " (Lever)")
	out := make([]domain.RawJob, 0, len(raws))
	for _, raw := range raws {
		var p leverPosting
		if err := json.Unmarshal(raw, &p); err != nil {
			return nil, fmt.Errorf("decode posting: %w", err)
		}
		var b strings.Builder
		b.WriteString(p.DescriptionPlain)
		for _, li := range p.Lists {
			b.WriteString("\n" + li.Text + "\n" + li.Content)
		}
		b.WriteString("\n" + p.AdditionalPlain)
		loc := p.Categories.Location
		if p.WorkplaceType == "remote" {
			loc += " (remote)"
		}
		salary := ""
		if p.SalaryRange != nil && p.SalaryRange.Currency != "" {
			salary = fmt.Sprintf("%.0f - %.0f %s", p.SalaryRange.Min, p.SalaryRange.Max, p.SalaryRange.Currency)
		}
		var pub *time.Time
		if p.CreatedAt > 0 {
			t := time.UnixMilli(p.CreatedAt).UTC()
			pub = &t
		}
		apply := p.ApplyURL
		if apply == "" {
			apply = p.HostedURL
		}
		out = append(out, domain.RawJob{ExternalJobID: p.ID, Company: company, Title: p.Text, Description: b.String(), Location: loc,
			EmploymentType: p.Categories.Commitment, SalaryText: salary, ApplicationURL: apply, PublishedAt: pub,
			RawContentType: "application/json", Raw: raw})
	}
	return out, nil
}
