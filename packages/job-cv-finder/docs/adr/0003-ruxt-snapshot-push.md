# ADR-0003: Ruxt pushes owner-selected candidate snapshots

**Status:** accepted (2026-11-20)

**Context.** The brief asks for an integration service that reads profiles and CVs from Ruxt. Verified: Ruxt stores CVs and profiles only in the browser, its MCP CV tools return "unavailable", cloud sync is planned but not built, and its non-goals forbid automatic upload of standalone profiles.

**Decision.** The Ruxt `/jobs` feature builds a `candidate-snapshot` (v1) in the browser from the profile and CVs the owner selects, with each CV's id and revision, and sends it through Ruxt's server proxy. The core stores it read-only, deduplicated by content hash. All facts cite `profile:<id>@<updated_at>` or `cv:<id>@<revision>`. Generated documents return to Ruxt as **new** local CVs when the owner opens them. Originals are never modified.

**Consequences.** + Ruxt remains the sole authority, and its local-first guarantees hold. + No fake Ruxt endpoints. − The core only sees what was last sent; Overview and integration health show the snapshot age, and the policy blocks submissions on data older than 30 days. If Ruxt ships owner-scoped cloud sessions later, a pull adapter can replace the push without changing the contract.
