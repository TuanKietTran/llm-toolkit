# 009 OAuth

Generic OAuth 2.0 authorization-code + PKCE (S256) for providers that have an `oauth` block: `auth_url`, `token_url`, `client_id`, optional `client_secret` (`env:` ok), `scopes`, `extra_params`.

- REQ-009-1: `POST /admin/oauth/{id}/start` returns the authorization URL with `state`, `code_challenge`, `redirect_uri`. Pending attempts live 10 min in memory and are single-use.
- REQ-009-2: Redirect URI = `{public_url}/oauth/callback`, falling back to the request host (honours `X-Forwarded-Proto`). It must be registered with the IdP.
- REQ-009-3: `GET /oauth/callback` exchanges the code (with `code_verifier`) and stores tokens. Unauthenticated, protected by `state`.
- REQ-009-4: Before each upstream call the access token is refreshed if it expires within 60s; refreshes are serialised per provider; a missing refresh token is kept when the IdP does not rotate it.
- REQ-009-5: Token injection: `openai` type -> `Authorization: Bearer`; `anthropic` type -> `Authorization: Bearer` (instead of `x-api-key`). Token failure counts as a provider failure (cooldown, fall to next target).
- REQ-009-6: `DELETE /admin/oauth/{id}?account=` removes credentials. All OAuth endpoints take an optional `account` (default `default`).

## Device-code flow (REQ-009-7..9)
Set `oauth.device_url` to enable RFC 8628. `POST /admin/oauth/{id}/device?account=` returns `user_code` and `verification_uri`; gorouter polls `token_url` in the background (honours `authorization_pending`, `slow_down`, expiry, denial; also accepts providers that return errors with HTTP 200 such as GitHub). `GET .../device` returns `{state: pending|connected|error}`. A new attempt supersedes the previous one for that account.

## Scope note
gorouter ships no client IDs. Bring your own OAuth app registered with the provider. Reusing another product's first-party client ID is out of scope. Use the device-code flow where the provider supports it.
