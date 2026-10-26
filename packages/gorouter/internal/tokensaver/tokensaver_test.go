package tokensaver

import (
	"strings"
	"testing"

	"gorouter/internal/config"
)

func TestCompress(t *testing.T) {
	var lb strings.Builder
	for i := 0; i < 2000; i++ {
		lb.WriteString("row number " + strings.Repeat("x", i%7) + string(rune('a'+i%26)) + "\n")
	}
	long := lb.String()
	var db strings.Builder
	db.WriteString("diff --git a/x b/x\n--- a/x\n+++ b/x\n@@ -1,40 +1,40 @@\n")
	for i := 1; i <= 10; i++ {
		db.WriteString(" c" + string(rune('A'+i)) + " unchanged context line number\n")
	}
	db.WriteString("-old\n+new\n")
	for i := 1; i <= 10; i++ {
		db.WriteString(" d" + string(rune('A'+i)) + " unchanged context line number\n")
	}
	diff := db.String()
	tests := []struct {
		name, in string
		max      int
		diff     bool
		want     []string // substrings that must appear
		not      []string // substrings that must not appear
	}{
		{"ansi", "\x1b[31mred\x1b[0m text", 1000, false, []string{"red text"}, []string{"\x1b"}},
		{"trailing ws + blank runs", "a   \n\n\n\nb\t", 1000, false, []string{"a\n\nb"}, []string{"\n\n\n"}},
		{"dupes", "x\nx\nx\nx\ny", 1000, false, []string{"x (x4)", "y"}, nil},
		{"two dupes untouched", "x\nx\ny", 1000, false, []string{"x\nx\ny"}, []string{"(x2)"}},
		{"truncate", long, 1000, false, []string{"chars truncated"}, nil},
		{"diff trimmed", diff, 1000, true, []string{"-old", "+new", " cK unchanged", " dB unchanged", "context lines omitted"}, []string{" cB unchanged", " dK unchanged"}},
		{"diff kept when off", diff, 1000, false, []string{" cB unchanged", " dK unchanged"}, nil},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := Compress(tc.in, tc.max, tc.diff)
			for _, w := range tc.want {
				if !strings.Contains(got, w) {
					t.Errorf("missing %q in %q", w, got)
				}
			}
			for _, n := range tc.not {
				if strings.Contains(got, n) {
					t.Errorf("unexpected %q in %q", n, got)
				}
			}
			if len(got) > len(tc.in) {
				t.Errorf("output grew: %d > %d", len(got), len(tc.in))
			}
		})
	}
	if got := Compress(long, 1000, false); len(got) > 1100 {
		t.Errorf("truncated result too long: %d", len(got))
	}
}

func TestNeverGrows(t *testing.T) {
	if got := Compress("short", 3, false); got != "short" && len(got) > len("short") {
		t.Fatalf("grew: %q", got)
	}
}

func TestApply(t *testing.T) {
	body := map[string]any{"messages": []any{
		map[string]any{"role": "user", "content": strings.Repeat("u\n", 5000)},
		map[string]any{"role": "tool", "content": strings.Repeat("line\n", 5000)},
		map[string]any{"role": "tool", "content": []any{map[string]any{"type": "text", "text": "a\na\na\na"}}},
	}}
	saved := Apply(body, config.TokenSaver{Enabled: true, MaxToolChars: 500, TersePrompt: "Be brief."})
	if saved < 20000 {
		t.Fatalf("saved too little: %d", saved)
	}
	msgs := body["messages"].([]any)
	if msgs[0].(map[string]any)["role"] != "system" || msgs[0].(map[string]any)["content"] != "Be brief." {
		t.Fatalf("terse system message missing: %v", msgs[0])
	}
	if len(msgs[1].(map[string]any)["content"].(string)) != 10000 {
		t.Fatal("user message must not be touched")
	}
	if !strings.Contains(msgs[3].(map[string]any)["content"].([]any)[0].(map[string]any)["text"].(string), "(x4)") {
		t.Fatal("text part not compressed")
	}

	// Existing system message gets the prompt appended once.
	b2 := map[string]any{"messages": []any{map[string]any{"role": "system", "content": "sys"}}}
	Apply(b2, config.TokenSaver{TersePrompt: "T"})
	Apply(b2, config.TokenSaver{TersePrompt: "T"})
	if got := b2["messages"].([]any)[0].(map[string]any)["content"]; got != "sys\n\nT" {
		t.Fatalf("got %q", got)
	}
}

func TestFailOpen(t *testing.T) {
	for _, b := range []map[string]any{
		{}, {"messages": "nope"}, {"messages": []any{nil, 3, map[string]any{"role": "tool", "content": 7}}},
	} {
		if Apply(b, config.TokenSaver{}) != 0 {
			t.Fatalf("saved something from %v", b)
		}
	}
}
