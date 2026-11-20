# ADR-0001: Go modular monolith core, Python workers over HTTP

**Status:** accepted (2026-11-20)

**Context.** The workflow needs durable state, idempotency and crash recovery. Intelligence work (fact extraction, matching, generation) benefits from Python's ecosystem and changes faster. The existing job-cv-finder had no durable state.

**Decision.** One Go process (`jcf`) owns the API, workflow, scheduler, policy, execution and persistence. Python workers pull tasks over an authenticated HTTP lease API and return JSON validated against shared JSON Schemas. Python never touches the database or credentials.

**Consequences.** + Atomic state transitions with enqueue. + Workers are stateless and replaceable. − Two languages to maintain; contracts must change in lockstep (both test suites load the same schema files).
