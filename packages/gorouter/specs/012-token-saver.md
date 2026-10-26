# 012 Token saver

Optional request middleware that shrinks the prompt before routing. Config block `token_saver`:

| Field | Default | Meaning |
|-------|---------|---------|
| `enabled` | false | master switch |
| `max_tool_chars` | 8000 | cap per tool message; longer output keeps ~60% head + ~40% tail around `[... N chars truncated ...]` |
| `trim_diff_context` | false | for unified diffs keep headers, `@@` lines, changed lines and 1 line of context; omitted runs become `[... N context lines omitted ...]` |
| `terse_prompt` | "" | text appended once to the system message (or inserted as a new first system message) |

- REQ-012-1: Runs in `server.chat` on the pivot (OpenAI) body, after Anthropic->pivot conversion and before routing, so it applies to every client format and provider.
- REQ-012-2: Only messages with `role: "tool"` (string content or `text` parts) are rewritten. User/assistant content is never touched.
- REQ-012-3: Per tool message: strip ANSI escapes, trim trailing whitespace, collapse blank-line runs to one blank line, fold 3+ identical consecutive lines into `line (xN)`, then optional diff trimming, then the length cap.
- REQ-012-4: Fail-open: the result is never longer than the input, panics are recovered, and unexpected shapes are left as-is.
- REQ-012-5: Characters removed are counted since process start and exposed as `token_saver.saved_chars` in `GET /admin/status`.

## Extension
Spec [015](015-token-optimization.md) adds cache-control passthrough, safe consecutive tool deduplication, bounded history, per-route budgets, and a savings breakdown.

## Not included
Semantic compression (summarising history) and the external "headroom" style compress proxy are not implemented.
