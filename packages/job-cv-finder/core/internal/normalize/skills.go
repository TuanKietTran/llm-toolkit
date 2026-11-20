package normalize

import (
	"regexp"
	"sort"
	"strings"
)

// skillLexicon maps a canonical skill name to the spellings that indicate it. It is a
// deterministic first pass; the Python worker may add more nuanced requirement parsing.
var skillLexicon = map[string][]string{
	"Go":               {"golang", "go lang", "go developer", "go engineer", "(go)", " go,", "go/", "/go"},
	"Python":           {"python"},
	"TypeScript":       {"typescript"},
	"JavaScript":       {"javascript"},
	"Node.js":          {"node.js", "nodejs"},
	"NestJS":           {"nestjs"},
	"React":            {"react", "reactjs"},
	"Vue":              {"vue", "vuejs", "nuxt"},
	"Java":             {"java "},
	"C#":               {"c#", ".net"},
	"Rust":             {"rust"},
	"C++":              {"c++"},
	"SQL":              {"sql"},
	"PostgreSQL":       {"postgres", "postgresql"},
	"MySQL":            {"mysql"},
	"MongoDB":          {"mongodb", "mongo"},
	"Redis":            {"redis"},
	"Kafka":            {"kafka"},
	"Docker":           {"docker"},
	"Kubernetes":       {"kubernetes", "k8s"},
	"AWS":              {"aws", "amazon web services"},
	"GCP":              {"gcp", "google cloud"},
	"Azure":            {"azure"},
	"Terraform":        {"terraform"},
	"CI/CD":            {"ci/cd", "cicd", "github actions", "gitlab ci"},
	"Linux":            {"linux"},
	"gRPC":             {"grpc"},
	"GraphQL":          {"graphql"},
	"LLM":              {"llm", "large language model", "generative ai", "genai", "gpt"},
	"RAG":              {"rag", "retrieval augmented", "retrieval-augmented"},
	"Machine Learning": {"machine learning", " ml ", "ml engineer"},
	"Spark":            {"spark", "pyspark"},
	"Airflow":          {"airflow"},
	"dbt":              {"dbt"},
	"Odoo":             {"odoo"},
	"Microservices":    {"microservice"},
}

var rePreferredHeading = regexp.MustCompile(`(?im)^.*(nice to have|preferred|bonus|a plus|plus point|good to have|điểm cộng|ưu tiên).*$`)
var reRequiredHeading = regexp.MustCompile(`(?im)^.*(requirement|qualification|must have|what you.ll need|yêu cầu|your skills).*$`)

// Skills splits lexicon hits into required and preferred. Text after a "nice to have"-style
// heading counts as preferred until a requirements heading appears again. Tags the source
// itself lists (ITviec skill chips) count as required.
func Skills(description string, sourceTags []string) (required, preferred []string) {
	req := map[string]bool{}
	pref := map[string]bool{}
	for _, t := range sourceTags {
		if c := canonicalSkill(t); c != "" {
			req[c] = true
		}
	}
	lower := " " + strings.ToLower(description) + " "
	// Mark preferred spans.
	type span struct{ start, end int }
	var prefSpans []span
	prefIdx := rePreferredHeading.FindAllStringIndex(lower, -1)
	reqIdx := reRequiredHeading.FindAllStringIndex(lower, -1)
	for _, p := range prefIdx {
		end := len(lower)
		for _, r := range reqIdx {
			if r[0] > p[1] && r[0] < end {
				end = r[0]
			}
		}
		prefSpans = append(prefSpans, span{p[0], end})
	}
	inPref := func(i int) bool {
		for _, s := range prefSpans {
			if i >= s.start && i < s.end {
				return true
			}
		}
		return false
	}
	for canon, spellings := range skillLexicon {
		for _, sp := range spellings {
			for _, idx := range wordIndexes(lower, sp) {
				if inPref(idx) {
					if !req[canon] {
						pref[canon] = true
					}
				} else {
					req[canon] = true
					delete(pref, canon)
				}
			}
		}
	}
	for k := range req {
		required = append(required, k)
	}
	for k := range pref {
		if !req[k] {
			preferred = append(preferred, k)
		}
	}
	sort.Strings(required)
	sort.Strings(preferred)
	return required, preferred
}

// wordIndexes finds occurrences of needle not embedded inside a longer alphanumeric word.
func wordIndexes(hay, needle string) []int {
	var out []int
	for off := 0; ; {
		i := strings.Index(hay[off:], needle)
		if i < 0 {
			return out
		}
		pos := off + i
		before := pos == 0 || !isWordByte(hay[pos-1]) || !isWordByte(needle[0])
		endPos := pos + len(needle)
		after := endPos >= len(hay) || !isWordByte(hay[endPos]) || !isWordByte(needle[len(needle)-1])
		if before && after {
			out = append(out, pos)
		}
		off = pos + len(needle)
	}
}

func isWordByte(b byte) bool {
	return b >= 'a' && b <= 'z' || b >= '0' && b <= '9'
}

// canonicalSkill maps a free-form tag ("ReactJS", "NodeJS") onto the lexicon name.
func canonicalSkill(tag string) string {
	t := " " + strings.ToLower(strings.TrimSpace(tag)) + " "
	for canon, spellings := range skillLexicon {
		if strings.EqualFold(strings.TrimSpace(tag), canon) {
			return canon
		}
		for _, sp := range spellings {
			if len(wordIndexes(t, strings.TrimSpace(sp))) > 0 && len(strings.TrimSpace(sp)) >= 2 {
				return canon
			}
		}
	}
	tag = strings.TrimSpace(tag)
	if tag == "" || len(tag) > 40 {
		return ""
	}
	return tag // keep unknown source tags verbatim rather than dropping them
}
