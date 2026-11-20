# ADR-0004: Durable task queue in the workflow database; HTTP, not gRPC

**Status:** accepted (2026-11-20)

**Decision.** Tasks are rows (`tasks`) with leases, attempts, backoff (`not_before`) and a dead state. They are enqueued in the same transaction as the transition that needs them. Workers use plain HTTP + JSON.

**Why.** It avoids a dual write between database and broker (no outbox needed), redelivers on worker crash via lease expiry, and dead-letters into an explicit candidate exception. The load is a few tasks per hour, so gRPC/Kafka/Redis/Temporal would add operations without benefit.

**Revisit when** several core instances or high task volume are needed.
