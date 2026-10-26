# 004 Translation

Pivot = OpenAI chat completions.

## Request OpenAI -> Anthropic (REQ-004-1)
- `system`/`developer` messages -> `system` string.
- Consecutive same-role messages merge; `tool` messages become `tool_result` blocks in a user message.
- assistant `tool_calls` -> `tool_use` blocks (arguments JSON parsed).
- `image_url` -> `image` (base64 for `data:` URLs, else url source).
- `max_tokens`/`max_completion_tokens` (default 4096), `stop` -> `stop_sequences`, `tools` -> `input_schema`.
- `tool_choice`: auto->auto, required->any, named->tool, none->tools omitted.

## Request Anthropic -> OpenAI (REQ-004-2)
Inverse of the above; `tool_result` blocks become `tool` messages preceding the user content.

## Responses (REQ-004-3)
`stop_reason` <-> `finish_reason`: end_turn/stop_sequence=stop, max_tokens=length, tool_use=tool_calls. Usage mapped input/output <-> prompt/completion.

## Streaming (REQ-004-4)
Every upstream stream becomes OpenAI chunks; the client writer renders chunks as OpenAI SSE (+`[DONE]`) or Anthropic events (`message_start`, `content_block_*`, `message_delta`, `message_stop`). For OpenAI upstreams `stream_options.include_usage` is forced on so usage is captured.

## Gemini / Vertex (REQ-004-5)
Request OpenAI -> Gemini `generateContent` (the model lives in the URL):
- `system`/`developer` -> `systemInstruction`; `assistant` -> role `model`; consecutive same-role turns merge (so several tool results become one user turn of `functionResponse` parts).
- `image_url`: `data:` URLs -> `inlineData`, http(s) -> `fileData`.
- assistant `tool_calls` -> `functionCall{name,args}`; `tool` messages -> `functionResponse`, name resolved from the earlier `tool_call_id` (fallback: message `name`); non-object results are wrapped as `{"result": text}`.
- `temperature`/`top_p`/`max_tokens`/`stop` -> `generationConfig` (`temperature`, `topP`, `maxOutputTokens`, `stopSequences`).
- `tools` -> `functionDeclarations`; unsupported schema keys (`additionalProperties`, `$schema`, `$id`, `$ref`, `definitions`, `$defs`, `strict`) are stripped recursively and `["T","null"]` types become `T` + `nullable`.
- `tool_choice`: auto->AUTO, required->ANY, none->NONE, named->ANY + `allowedFunctionNames`.

Response: text parts (parts with `thought: true` skipped) and `functionCall` -> `tool_calls` with generated `call_*` ids. `finishReason`: STOP=stop (tool_calls if any call), MAX_TOKENS=length, SAFETY/RECITATION/BLOCKLIST/PROHIBITED_CONTENT/SPII=content_filter. Usage from `usageMetadata`.

Stream (`:streamGenerateContent?alt=sse`): a role chunk, then text / tool-call deltas per event (each `functionCall` arrives complete, so it is emitted whole with an incrementing `index`), then one finishing chunk with the finish reason and usage.

## Known gaps
Thinking/reasoning blocks, prompt-cache controls, Gemini grounding/safety settings, Cursor/Kiro formats (see TODO "Won't do").
