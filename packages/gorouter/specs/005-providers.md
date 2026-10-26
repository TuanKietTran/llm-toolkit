# 005 Providers

Extra fields: `accounts`, `account_strategy`, `quotas` (see [011](011-accounts-quotas.md)).

Config fields (`internal/config.Provider`): `id`, `type` (`openai`|`anthropic`|`gemini`|`vertex`), `base_url`, `api_key` (literal or `env:NAME`), `headers`, `tier`, `models`, `price_in`/`price_out` (USD per 1M tokens), `disabled`.

- REQ-005-1: `openai` type posts to `{base_url}/chat/completions` with Bearer auth: covers OpenAI, GLM, MiniMax, Ollama, vLLM, OpenRouter, any compatible server.
- REQ-005-2: `anthropic` type posts to `{base_url}/v1/messages` with `x-api-key` and `anthropic-version: 2023-06-01`; default base `https://api.anthropic.com`.
- REQ-005-5: `gemini` type posts to `{base_url}/models/{model}:generateContent` (stream: `:streamGenerateContent?alt=sse`); default base `https://generativelanguage.googleapis.com/v1beta`; the key goes in `x-goog-api-key` (an OAuth token goes in `Authorization: Bearer`).
- REQ-005-6: `vertex` type uses the same URL scheme under a user-supplied `base_url` (e.g. `https://us-central1-aiplatform.googleapis.com/v1/projects/P/locations/L/publishers/google`) and always authenticates with `Authorization: Bearer` (static `api_key` access token or OAuth). gorouter does not mint tokens from service-account files; supply a token or configure `oauth`.
- REQ-005-3: Provider ids contain no `/` and never collide with combo names.
- REQ-005-4: Adding a provider type = new case in `provider.BuildRequest`, a reader in `translate`, a spec row here.

## Vertex service accounts (REQ-005-5..8)
- REQ-005-5: `vertex` providers accept `service_account` (provider shorthand for the default account, or per account): a key file path, raw JSON, or `env:NAME` holding either.
- REQ-005-6: gorouter signs an RS256 JWT (`iss`=client_email, scope `cloud-platform`, `aud`=`token_uri`, 1h) and exchanges it at `token_uri` (JWT-bearer grant); the access token is cached until 60s before expiry, one source per account, shared across config reloads (`internal/gcpauth`).
- REQ-005-7: `project` (+ optional `location`, default `us-central1`; `global` uses the bare host) builds `base_url` automatically.
- REQ-005-8: Keys are loaded and validated when the config is parsed, so a bad path or key rejects a hot-reload instead of failing at request time. `service_account` is rejected on non-vertex providers. Only RSA keys (Google's format) are supported.
