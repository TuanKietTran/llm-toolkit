// Package normalize turns adapter output (domain.RawJob) into canonical domain.Job values
// and computes the deterministic keys used for deduplication. Everything here is pure and
// deterministic; semantic similarity is only a secondary, flagged signal (see Similar).
package normalize

import (
	"crypto/sha256"
	"encoding/hex"
	"html"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode"

	"golang.org/x/text/runes"
	"golang.org/x/text/transform"
	"golang.org/x/text/unicode/norm"

	"github.com/TuanKietTran/llm-toolkit/packages/job-cv-finder/core/internal/domain"
)

var (
	reTags     = regexp.MustCompile(`(?is)<(script|style)[^>]*>.*?</(script|style)>|<[^>]+>`)
	reBlock    = regexp.MustCompile(`(?i)</?(p|div|li|ul|ol|br|h[1-6]|tr|section)[^>]*>`)
	reSpaces   = regexp.MustCompile(`[ \t\f\v]+`)
	reNewlines = regexp.MustCompile(`\n{3,}`)
	reNonWord  = regexp.MustCompile(`[^a-z0-9+#.\s]`)
	reMultiSp  = regexp.MustCompile(`\s+`)
	reMoney    = regexp.MustCompile(`(?i)(?:up to\s*)?([\d.,]+)\s*(k|m|tr|triệu)?\s*(?:-|–|to)?\s*([\d.,]+)?\s*(k|m|tr|triệu)?\s*(usd|vnd|\$|đ|eur|sgd)?`)
	reYears    = regexp.MustCompile(`(?i)(\d{1,2})\s*\+?\s*(?:-\s*\d{1,2}\s*)?(?:years?|yrs?|năm)`)
)

// StripHTML converts an HTML fragment to readable plain text, keeping block breaks.
func StripHTML(s string) string {
	s = html.UnescapeString(s) // Greenhouse double-encodes content
	s = reBlock.ReplaceAllString(s, "\n")
	s = reTags.ReplaceAllString(s, "")
	s = html.UnescapeString(s)
	lines := strings.Split(s, "\n")
	for i, l := range lines {
		lines[i] = strings.TrimSpace(reSpaces.ReplaceAllString(l, " "))
	}
	s = strings.Join(lines, "\n")
	return strings.TrimSpace(reNewlines.ReplaceAllString(s, "\n\n"))
}

// fold lowercases and strips diacritics so "Hồ Chí Minh" == "ho chi minh".
func fold(s string) string {
	t := transform.Chain(norm.NFD, runes.Remove(runes.In(unicode.Mn)), norm.NFC)
	out, _, err := transform.String(t, s)
	if err != nil {
		out = s
	}
	out = strings.ReplaceAll(out, "đ", "d")
	out = strings.ReplaceAll(out, "Đ", "D")
	return strings.ToLower(out)
}

var companySuffixes = []string{
	"co., ltd", "co.,ltd", "co ltd", "company limited", "limited", "ltd", "llc", "inc", "jsc",
	"joint stock company", "corporation", "corp", "pte", "pte ltd", "gmbh", "plc", "group",
	"vietnam", "viet nam", "vn",
}

// Company produces the company dedup key: folded, punctuation-free, legal suffixes and a
// trailing country name removed. "giftee Tech Vietnam" and "Giftee Tech" collide on purpose.
func Company(s string) string {
	c := fold(s)
	c = strings.NewReplacer("&", " and ", "|", " ", "–", " ", "-", " ").Replace(c)
	c = reNonWord.ReplaceAllString(c, " ")
	c = strings.TrimSpace(reMultiSp.ReplaceAllString(c, " "))
	for changed := true; changed; {
		changed = false
		for _, suf := range companySuffixes {
			s := strings.ReplaceAll(suf, ",", "")
			s = strings.ReplaceAll(s, ".", "")
			c2 := strings.TrimSpace(strings.TrimSuffix(strings.ReplaceAll(c, ".", ""), " "+s))
			if c2 != strings.ReplaceAll(c, ".", "") && c2 != "" {
				c, changed = c2, true
			}
		}
	}
	return strings.ReplaceAll(c, ".", "")
}

// Title produces the title dedup key: folded, punctuation collapsed, seniority words kept
// (a Senior and a Junior role at one company are different jobs).
func Title(s string) string {
	t := fold(s)
	t = strings.NewReplacer("/", " ", "(", " ", ")", " ", "[", " ", "]", " ", ",", " ", "-", " ", "–", " ", "|", " ").Replace(t)
	t = reNonWord.ReplaceAllString(t, " ")
	return strings.TrimSpace(reMultiSp.ReplaceAllString(t, " "))
}

// CanonicalKey is the deterministic cross-source dedup key.
func CanonicalKey(company, title string) string {
	return Company(company) + "|" + Title(title)
}

// Location maps common Vietnamese spellings onto one canonical city name.
func Location(s string) string {
	f := fold(s)
	switch {
	case f == "":
		return ""
	case strings.Contains(f, "ho chi minh") || strings.Contains(f, "hcm") || strings.Contains(f, "saigon") || strings.Contains(f, "sai gon") || regexp.MustCompile(`\bdistrict \d+\b`).MatchString(f) || strings.Contains(f, "thu duc"):
		return "Ho Chi Minh City"
	case strings.Contains(f, "ha noi") || strings.Contains(f, "hanoi"):
		return "Hanoi"
	case strings.Contains(f, "da nang") || strings.Contains(f, "danang"):
		return "Da Nang"
	case strings.Contains(f, "remote") && !strings.Contains(f, ","):
		return "Remote"
	}
	return strings.TrimSpace(s)
}

// WorkMode infers onsite/hybrid/remote from location + title + description text.
func WorkMode(location, title, description string) string {
	f := fold(location + " " + title + " " + firstN(description, 4000))
	switch {
	case strings.Contains(f, "hybrid"):
		return "hybrid"
	case strings.Contains(f, "fully remote") || strings.Contains(f, "100% remote") || strings.Contains(fold(location), "remote") || strings.Contains(fold(title), "remote"):
		return "remote"
	case strings.Contains(f, "on-site") || strings.Contains(f, "onsite") || strings.Contains(f, "at office") || strings.Contains(f, "tai van phong"):
		return "onsite"
	}
	return "unknown"
}

func firstN(s string, n int) string {
	if len(s) > n {
		return s[:n]
	}
	return s
}

// Salary parses texts such as "1,500 - 2,500 USD", "Up to 3000$", "20 - 30 triệu".
// Anything it cannot read confidently ("Negotiable", "You'll love it") yields nils.
func Salary(text string) (minV, maxV *float64, currency string) {
	f := strings.ToLower(strings.TrimSpace(text))
	if f == "" {
		return nil, nil, ""
	}
	m := reMoney.FindStringSubmatch(f)
	if m == nil || m[1] == "" {
		return nil, nil, ""
	}
	cur := m[5]
	switch cur {
	case "$":
		cur = "usd"
	case "đ":
		cur = "vnd"
	}
	if m[2] == "tr" || m[2] == "triệu" || m[4] == "tr" || m[4] == "triệu" {
		cur = "vnd"
	}
	if cur == "" {
		return nil, nil, "" // a bare number is too ambiguous to persist as salary
	}
	parse := func(num, unit string) *float64 {
		if num == "" {
			return nil
		}
		n := strings.ReplaceAll(num, ",", "")
		if strings.Count(n, ".") > 1 || (cur == "vnd" && strings.Count(n, ".") == 1 && len(n)-strings.Index(n, ".") == 4) {
			n = strings.ReplaceAll(n, ".", "")
		}
		v, err := strconv.ParseFloat(n, 64)
		if err != nil {
			return nil
		}
		switch unit {
		case "k":
			v *= 1e3
		case "m":
			v *= 1e6
		case "tr", "triệu":
			v *= 1e6
		}
		return &v
	}
	unitHi := m[4]
	unitLo := m[2]
	if unitLo == "" {
		unitLo = unitHi
	}
	lo := parse(m[1], unitLo)
	hi := parse(m[3], unitHi)
	if strings.HasPrefix(f, "up to") {
		return nil, lo, strings.ToUpper(cur)
	}
	return lo, hi, strings.ToUpper(cur)
}

// Experience extracts the first "N+ years" requirement, if stated.
func Experience(description string) string {
	if m := reYears.FindStringSubmatch(description); m != nil {
		return m[1] + "+ years"
	}
	return ""
}

// ContentHash fingerprints the fields that matter for change detection.
func ContentHash(j domain.Job) string {
	h := sha256.New()
	for _, s := range []string{j.CompanyNorm, j.TitleNorm, j.Description, j.Location, j.ApplicationURL, strings.Join(j.RequiredSkills, ","), strings.Join(j.PreferredSkills, ",")} {
		h.Write([]byte(s))
		h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil))
}

// Normalize builds the canonical job. now is injected for deterministic tests.
func Normalize(sourceID string, r domain.RawJob, now time.Time) domain.Job {
	desc := StripHTML(r.Description)
	req, pref := Skills(desc, r.Skills)
	loc := Location(r.Location)
	j := domain.Job{
		SourceID:              sourceID,
		ExternalJobID:         strings.TrimSpace(r.ExternalJobID),
		Company:               strings.TrimSpace(r.Company),
		Title:                 strings.TrimSpace(r.Title),
		Description:           desc,
		Location:              loc,
		WorkMode:              WorkMode(r.Location, r.Title, desc),
		EmploymentType:        strings.ToLower(strings.TrimSpace(r.EmploymentType)),
		RequiredSkills:        req,
		PreferredSkills:       pref,
		ExperienceRequirement: Experience(desc),
		ApplicationURL:        strings.TrimSpace(r.ApplicationURL),
		PublishedAt:           r.PublishedAt,
		CollectedAt:           now,
		LastSeenAt:            now,
		Status:                "active",
		Completeness:          "full",
		Questions:             r.Questions,
	}
	if r.Partial || desc == "" {
		j.Completeness = "partial"
	}
	j.CompanyNorm = Company(j.Company)
	j.TitleNorm = Title(j.Title)
	j.CanonicalKey = j.CompanyNorm + "|" + j.TitleNorm
	j.SalaryMin, j.SalaryMax, j.SalaryCurrency = Salary(r.SalaryText)
	if strings.Contains(fold(desc), "english") {
		j.LanguageRequirement = "English mentioned"
	}
	j.ContentHash = ContentHash(j)
	return j
}

// Similar is the SECONDARY dedup signal: same company key and title token Jaccard >= 0.8.
// It never merges jobs by itself; callers record it as a "probable duplicate" hint.
func Similar(a, b domain.Job) bool {
	if a.CompanyNorm == "" || a.CompanyNorm != b.CompanyNorm {
		return false
	}
	ta, tb := strings.Fields(a.TitleNorm), strings.Fields(b.TitleNorm)
	if len(ta) == 0 || len(tb) == 0 {
		return false
	}
	inter := 0
	for _, t := range ta {
		if slices.Contains(tb, t) {
			inter++
		}
	}
	union := len(ta) + len(tb) - inter
	return float64(inter)/float64(union) >= 0.8
}
