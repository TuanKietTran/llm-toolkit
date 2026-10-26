// Package translate converts requests, responses and streams between the
// OpenAI chat-completions format (the internal pivot) and the Anthropic
// messages format.
package translate

import (
	"bufio"
	"encoding/json"
	"io"
	"strings"
)

func asMap(v any) map[string]any {
	m, _ := v.(map[string]any)
	return m
}

func asSlice(v any) []any {
	s, _ := v.([]any)
	return s
}

func str(v any) string {
	s, _ := v.(string)
	return s
}

func num(v any) int {
	switch n := v.(type) {
	case float64:
		return int(n)
	case int:
		return n
	}
	return 0
}

// contentText flattens string-or-parts content into plain text.
func contentText(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	var parts []string
	for _, p := range asSlice(v) {
		pm := asMap(p)
		if t := str(pm["text"]); t != "" {
			parts = append(parts, t)
		}
	}
	return strings.Join(parts, "\n")
}

func parseJSONObject(s string) any {
	if strings.TrimSpace(s) == "" {
		return map[string]any{}
	}
	var v any
	if err := json.Unmarshal([]byte(s), &v); err != nil {
		return map[string]any{}
	}
	return v
}

func toJSON(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

// ReadSSE parses a server-sent-events stream, invoking fn per event.
func ReadSSE(r io.Reader, fn func(event, data string) error) error {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64*1024), 8*1024*1024)
	var event string
	var data []string
	flush := func() error {
		if len(data) == 0 {
			event = ""
			return nil
		}
		err := fn(event, strings.Join(data, "\n"))
		event, data = "", nil
		return err
	}
	for sc.Scan() {
		line := sc.Text()
		switch {
		case line == "":
			if err := flush(); err != nil {
				return err
			}
		case strings.HasPrefix(line, ":"):
		case strings.HasPrefix(line, "event:"):
			event = strings.TrimSpace(line[6:])
		case strings.HasPrefix(line, "data:"):
			data = append(data, strings.TrimPrefix(strings.TrimPrefix(line, "data:"), " "))
		}
	}
	if err := flush(); err != nil {
		return err
	}
	return sc.Err()
}
