// Package tokensaver shrinks request payloads before routing. It is fail-open:
// malformed or unexpected request shapes are left unchanged.
package tokensaver

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	"gorouter/internal/config"
)

const defaultMaxChars = 8000

var ansiRe = regexp.MustCompile(`\x1b\[[0-9;?]*[ -/]*[@-~]`)

type Savings struct{ Total, Tool, History, Dedupe int }

// Apply preserves the original API while returning total characters saved.
func Apply(body map[string]any, cfg config.TokenSaver) int { return ApplyDetailed(body, cfg).Total }

// ApplyDetailed applies bounded, cache-friendly transformations.
func ApplyDetailed(body map[string]any, cfg config.TokenSaver) (saved Savings) {
	defer func() {
		if recover() != nil {
			saved = Savings{}
		}
	}()
	msgs, ok := body["messages"].([]any)
	if !ok {
		return
	}
	max := cfg.MaxToolChars
	if max <= 0 {
		max = defaultMaxChars
	}
	for _, m := range msgs {
		mm, _ := m.(map[string]any)
		if mm == nil {
			continue
		}
		if mm["role"] == "tool" {
			switch c := mm["content"].(type) {
			case string:
				out := Compress(c, max, cfg.TrimDiffContext)
				saved.Tool += len(c) - len(out)
				mm["content"] = out
			case []any:
				for _, p := range c {
					pm, _ := p.(map[string]any)
					if pm == nil {
						continue
					}
					if t, ok := pm["text"].(string); ok && pm["type"] == "text" {
						out := Compress(t, max, cfg.TrimDiffContext)
						saved.Tool += len(t) - len(out)
						pm["text"] = out
					}
				}
			}
		}
	}
	if cfg.DedupeToolMessages {
		saved.Dedupe += dedupe(&msgs)
	}
	if cfg.MaxHistoryMessages > 0 {
		saved.History += trimHistory(&msgs, cfg.MaxHistoryMessages)
	}
	if cfg.MaxPromptChars > 0 {
		saved.History += trimPromptChars(&msgs, cfg.MaxPromptChars)
	}
	if cfg.TersePrompt != "" {
		addTerse(body, msgs, cfg.TersePrompt)
		if updated, ok := body["messages"].([]any); ok {
			msgs = updated
		}
	}
	if cfg.CacheControl {
		addCacheControl(msgs)
	}
	body["messages"] = msgs
	saved.Total = saved.Tool + saved.History + saved.Dedupe
	return
}

func addTerse(body map[string]any, msgs []any, prompt string) {
	for _, m := range msgs {
		mm, _ := m.(map[string]any)
		if mm != nil && mm["role"] == "system" {
			if s, ok := mm["content"].(string); ok {
				if !strings.Contains(s, prompt) {
					mm["content"] = s + "\n\n" + prompt
				}
				return
			}
		}
	}
	body["messages"] = append([]any{map[string]any{"role": "system", "content": prompt}}, msgs...)
}

func addCacheControl(msgs []any) {
	for i := len(msgs) - 1; i >= 0; i-- {
		mm, _ := msgs[i].(map[string]any)
		if mm == nil {
			continue
		}
		role, _ := mm["role"].(string)
		if role != "system" {
			continue
		}
		if _, ok := mm["cache_control"]; !ok {
			mm["cache_control"] = map[string]any{"type": "ephemeral"}
		}
		if parts, ok := mm["content"].([]any); ok && len(parts) > 0 {
			if p, ok := parts[len(parts)-1].(map[string]any); ok {
				if _, exists := p["cache_control"]; !exists {
					p["cache_control"] = map[string]any{"type": "ephemeral"}
				}
			}
		}
		return
	}
}

func dedupe(msgs *[]any) int {
	in := *msgs
	out := make([]any, 0, len(in))
	var prev string
	duplicate := false
	saved := 0
	for _, m := range in {
		mm, _ := m.(map[string]any)
		role := ""
		if mm != nil {
			role, _ = mm["role"].(string)
		}
		b, _ := json.Marshal(m)
		key := string(b)
		if role == "tool" && duplicate && key == prev {
			saved += len(key)
			continue
		}
		out = append(out, m)
		duplicate = role == "tool"
		if duplicate {
			prev = key
		}
	}
	*msgs = out
	return saved
}

func trimHistory(msgs *[]any, max int) int {
	in := *msgs
	count := 0
	for _, m := range in {
		mm, _ := m.(map[string]any)
		if mm != nil && mm["role"] != "system" {
			count++
		}
	}
	if count <= max {
		return 0
	}
	remove := count - max
	out := make([]any, 0, len(in))
	saved := 0
	for _, m := range in {
		mm, _ := m.(map[string]any)
		role := ""
		if mm != nil {
			role, _ = mm["role"].(string)
		}
		if role != "system" && remove > 0 {
			b, _ := json.Marshal(m)
			saved += len(b)
			remove--
			continue
		}
		out = append(out, m)
	}
	*msgs = out
	return saved
}

func contentLen(m any) int { b, _ := json.Marshal(m); return len(b) }
func trimPromptChars(msgs *[]any, max int) int {
	in := *msgs
	total := 0
	for _, m := range in {
		total += contentLen(m)
	}
	if total <= max {
		return 0
	}
	out := make([]any, 0, len(in))
	saved := 0
	for i, m := range in {
		mm, _ := m.(map[string]any)
		role := ""
		if mm != nil {
			role, _ = mm["role"].(string)
		}
		remaining := len(in) - i
		if role != "system" && total > max && remaining > 1 {
			n := contentLen(m)
			total -= n
			saved += n
			continue
		}
		out = append(out, m)
	}
	*msgs = out
	return saved
}

// Compress applies cleanups and a head/tail cap without ever growing input.
func Compress(s string, maxChars int, trimDiff bool) string {
	orig := s
	s = ansiRe.ReplaceAllString(s, "")
	lines := strings.Split(s, "\n")
	for i := range lines {
		lines[i] = strings.TrimRight(lines[i], " \t\r")
	}
	if trimDiff && looksLikeDiff(lines) {
		lines = trimDiffContext(lines)
	}
	lines = collapse(lines)
	s = strings.TrimRight(strings.Join(lines, "\n"), "\n")
	if len(s) > maxChars && maxChars > 0 {
		s = truncate(s, maxChars)
	}
	if len(s) >= len(orig) {
		return orig
	}
	return s
}
func collapse(lines []string) []string {
	var out []string
	for i := 0; i < len(lines); {
		j := i
		for j < len(lines) && lines[j] == lines[i] {
			j++
		}
		n := j - i
		if lines[i] == "" {
			out = append(out, "")
		} else if n > 2 {
			out = append(out, fmt.Sprintf("%s (x%d)", lines[i], n))
		} else {
			out = append(out, lines[i:j]...)
		}
		i = j
	}
	return out
}
func looksLikeDiff(lines []string) bool {
	for _, l := range lines {
		if strings.HasPrefix(l, "@@ ") {
			return true
		}
	}
	return false
}
func trimDiffContext(lines []string) []string {
	changed := func(l string) bool { return strings.HasPrefix(l, "+") || strings.HasPrefix(l, "-") }
	keep := make([]bool, len(lines))
	h := false
	for i, l := range lines {
		switch {
		case strings.HasPrefix(l, "@@ "), strings.HasPrefix(l, "diff "), strings.HasPrefix(l, "index "):
			keep[i] = true
			h = strings.HasPrefix(l, "@@ ")
		case !h:
			keep[i] = true
		case changed(l):
			keep[i] = true
			if i > 0 {
				keep[i-1] = true
			}
			if i+1 < len(lines) {
				keep[i+1] = true
			}
		}
	}
	var out []string
	skip := 0
	flush := func() {
		if skip > 0 {
			out = append(out, fmt.Sprintf("[... %d context lines omitted ...]", skip))
			skip = 0
		}
	}
	for i, l := range lines {
		if keep[i] {
			flush()
			out = append(out, l)
		} else {
			skip++
		}
	}
	flush()
	return out
}
func truncate(s string, max int) string {
	head := max * 6 / 10
	tail := max - head
	if i := strings.LastIndex(s[:head], "\n"); i > head-200 && i > 0 {
		head = i
	}
	if i := strings.Index(s[len(s)-tail:], "\n"); i >= 0 && i < 200 {
		tail -= i + 1
	}
	cut := len(s) - head - tail
	return s[:head] + fmt.Sprintf("\n[... %d chars truncated ...]\n", cut) + s[len(s)-tail:]
}
