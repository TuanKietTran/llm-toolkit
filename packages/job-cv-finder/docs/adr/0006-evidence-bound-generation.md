# ADR-0006: Evidence-bound generation, validated twice

**Status:** accepted (2026-11-20)

**Decision.** Generated documents carry `statements[]`, each with evidence fact ids. Tailored CVs only reorder the source CV's lines. Cover letters come from a template or from a model that sees only evidenced statements, with the job text in an inert data block. Python validates (skills and numbers in a statement must appear in its cited evidence; no unevidenced skill anywhere except an explicit gap sentence) and falls back to the template on failure. Go re-checks that every statement has refs and that each ref exists in the snapshot's stored facts. Questions are answered only from candidate-written standing answers or evidenced facts; anything else stays `unknown` and blocks the application with a question for the candidate.

**Consequences.** + Fabrication by a model is caught deterministically (tested with an injecting fake model). − Letters are plain; quality improves with a model, but never at the cost of evidence.
