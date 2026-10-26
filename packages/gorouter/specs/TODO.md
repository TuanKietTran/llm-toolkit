# TODO

Work queue for gorouter. Move an item to Done in the same change that completes it, and update the matching spec.

## Open
- [ ] F7 Claude Code fidelity: `/v1/messages` always converts through the OpenAI pivot and drops `thinking`, `context_management`, `output_config`, `metadata` and `cache_control` (so no extended thinking or prompt-cache discount even against an Anthropic upstream); there is no `/v1/messages/count_tokens` route; server-side tools (no `input_schema`) are untested. Candidate fix: an Anthropic-to-Anthropic passthrough that forwards the body and `anthropic-beta` unchanged when the chosen target is an `anthropic` provider. Observed 2026-10-10, spec 014.

## Follow-ups noticed while building

## Won't do (with reason)
- Cursor / Kiro native formats: undocumented, reverse-engineered protocols tied to those products' private APIs. Reach them through an OpenAI-compatible bridge configured as a normal `openai` provider.
- Difficulty-based routing (RouteLLM style): needs a trained classifier; revisit as a separate experiment.

## Done
- [x] F6 Docker first run on a fresh volume: `/data` is now created in the build stage and copied into the distroless image owned by `nonroot` (65532), so the named volume inherits write access; verified compose up from a brand-new volume, 0 restarts, DB owned by 65532 — spec 014
- [x] F3 Docker/CI verified: first GitHub Actions run on main passed all jobs (test, e2e in Chrome, docker image build); image tags confirmed on registries
- [x] F5 Browser e2e of dashboard click paths (`make e2e`, spec 013); it caught missing hashchange handling and a stale-render race, both fixed
- [x] F4 Dashboard rendered in headless Brave: Overview, Providers, Config tabs screenshot-checked, no console errors; found and fixed failed requests not counted as errors (006) and misaligned numeric headers
- [x] F1 Persist quota windows to SQLite — spec 011/008
- [x] F2 Vertex service-account tokens (JWT bearer, cached) — spec 005
- [x] T1 Multiple accounts per provider (rotation + per-account cooldown, per-account OAuth) — spec 003/005/009
- [x] T2 Quota windows (5h / daily / weekly) per account, local tracking + dashboard view — spec 011
- [x] T5 Embeddings, speech, image and transcription (multipart) endpoints with fallback — spec 002
- [x] T6 Device-code OAuth flow — spec 009
- [x] T7 Dockerfile, compose, CI workflow
- [x] T8 Dashboard: accounts, quotas, saver stats, device-code UI — spec 010
- [x] T4 Token saver (compression, cache passthrough, dedupe, history trimming, route budgets, and savings telemetry) — spec 012/015
- [x] T3 Gemini + Vertex provider types (request/response/stream translation) — spec 004/005

Earlier: see git history / specs/007-roadmap.md for earlier work (core routing, translation, SQLite, OAuth PKCE, dashboard).
