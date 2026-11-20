# Operations runbook

## Health

| Check | Healthy |
|---|---|
| `GET /healthz` | `ok` (process up) |
| `GET /readyz` | 200 (database reachable) |
| `GET /metrics` | `jcf_tasks{state="dead"}` stays 0; `jcf_applications{state="BLOCKED_EXCEPTION"}` is work for the candidate, not an outage |
| Ruxt → Job sources | `health=ok` for polling sources; `failing` shows `last_error` |

Logs are JSON on stdout. Attributes whose keys look secret (`token`, `password`, `cookie`, `credential`, `session`, …) are redacted by the logger.

## Common situations

| Symptom | Meaning | Action |
|---|---|---|
| Applications stuck in `MATCHING`/`PREPARING` | No worker is running, or the worker cannot reach the core | Start `python -m jcf_intel`; check `JCF_CORE_URL` and the token; `GET /api/v1/tasks` shows queued/leased tasks |
| `BLOCKED_EXCEPTION`: "intelligence task … failed permanently" | Worker crashed or returned contract-invalid output five times | Read the task error (`/api/v1/tasks`), fix the worker, then **Retry** in Ruxt |
| `BLOCKED_EXCEPTION`: "missing information …" | A required application question has no evidence or standing answer | Answer in the application detail, or add a standing answer in Settings and send a snapshot |
| `SUBMISSION_UNKNOWN` | The ATS outcome was uncertain (timeout or 5xx) or the core restarted mid-submission | Nothing; the engine reconciles by lookup every 2 min. After 24 h it escalates to an exception: check the ATS manually, then **Retry** (safe: same idempotency key) or attest |
| Account `reauth_required` | The ATS rejected the stored credential | Ruxt → Platform accounts → save a new key; then **Retry** |
| `HANDOFF_PENDING` piling up | Expected for LinkedIn/ITviec | Apply on the platform; forward or upload the confirmation emails (`POST /api/v1/intake/emails`) |

## Recovery after a crash

Restart `jcf`. On the first tick:
- expired task leases are returned to the queue (or dead-lettered after max attempts);
- an application in `EXECUTING` that no live process owns moves to `SUBMISSION_UNKNOWN`, and its run is marked `unknown`;
- reconciliation then decides between submitted, safe retry and escalation.

No manual database edits are required. Tested by `TestCrashDuringSubmissionRecovers`.

## Backup and restore

The database is one SQLite file in WAL mode.

```sh
# online, consistent backup
sqlite3 /var/lib/jcf/jcf.db ".backup '/backups/jcf-$(date +%F).db'"
# restore: stop jcf, replace the file (remove stale -wal/-shm), start jcf
```

Back up `JCF_SECRET_KEY` separately in the secret manager. Without it the stored credentials cannot be decrypted (the vault refuses rather than guessing), and you must re-enter them.

## Retention

Raw job snapshots and inbound messages older than the source's `retention_days` can be pruned. Keep any row referenced by an application (`jobs.source_snapshot_id`, `application_documents.job_snapshot_id`):

```sql
DELETE FROM inbound_messages WHERE processed_at < datetime('now','-365 days');
DELETE FROM job_snapshots WHERE collected_at < datetime('now','-365 days')
  AND id NOT IN (SELECT source_snapshot_id FROM jobs WHERE source_snapshot_id IS NOT NULL)
  AND id NOT IN (SELECT job_snapshot_id FROM application_documents WHERE job_snapshot_id IS NOT NULL);
```

This is not yet scheduled automatically (see the status table in README).

## Rotating tokens and keys

- `JCF_API_TOKEN`: update the core and `NUXT_JOB_AUTOMATION_TOKEN` together, then restart both.
- `JCF_WORKER_TOKEN`: update the core and the workers.
- `JCF_SECRET_KEY`: credentials are bound to the key id. After rotation, re-enter each credential in Platform accounts.
