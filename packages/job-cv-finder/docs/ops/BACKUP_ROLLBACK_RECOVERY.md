# Backup, restore, rollback and incident recovery

## What protects the data

| Layer | Frequency | Retention | Where |
|---|---|---|---|
| SQLite `.backup` (WAL-aware, `PRAGMA integrity_check` on the copy, gzip) | every 6 hours (`jcf-backup.timer`) and before every deploy | 7 days on the data volume, 35 days in S3 (versioned, SSE-S3, TLS only, public access blocked) | `s3://<jcf-backup bucket>/db/` |
| EBS snapshot of the data volume (DLM policy, tag `Snapshot=jcf-daily`) | daily 03:30 UTC | 14 | EBS snapshots |
| Credential key `JCF_SECRET_KEY` | n/a | SSM SecureString `/jcf/env/JCF_SECRET_KEY` and Terraform state | without it stored platform credentials are unreadable and must be re-entered |

The database file is never inside a container layer: `/var/lib/jcf` is a separate XFS volume with `prevent_destroy` in Terraform.

## Verify a backup (safe, touches nothing in production)

```sh
/usr/local/sbin/jcf-restore verify            # newest backup
/usr/local/sbin/jcf-restore verify db/<key>   # a specific one
```

It downloads the object, runs `integrity_check` and prints table, job and application counts. The deploy workflow runs this on every deployment.

## Restore

1. Find the backup: `aws s3 ls s3://<bucket>/db/ --recursive | tail` (from SSM Session Manager on the host, or the console).
2. `/usr/local/sbin/jcf-restore apply db/<key>`. It verifies the copy first, stops `intel` and `core`, moves the current `jcf.db`, `-wal` and `-shm` into `/var/lib/jcf/replaced/` (nothing is deleted), installs the restored file and starts the services.
3. Check `GET /readyz` and the Ruxt overview.

If the whole volume is lost: create a volume from the newest `jcf-daily` snapshot, attach it to the instance as `/dev/sdf`, mount it at `/var/lib/jcf`, then redeploy. With Terraform, import or replace `aws_ebs_volume.data` deliberately (it is protected from accidental destroy).

## Roll back a release

Automatic: `deploy.sh` restores the previous image pair when the new core is not healthy within three minutes and the deployment fails.

Manual: re-run the workflow on the previous commit (`gh workflow run job-cv-finder-deploy.yml --ref <branch>` at a commit whose images exist; images are immutable `sha-<12>` tags and ECR keeps the newest 15). Database migrations are additive and run at core start after a pre-deploy backup; if a migration must be undone, restore the pre-deploy backup (`jcf-pre-deploy-*`) with `jcf-restore apply`.

## Incidents

| Symptom | First checks | Action |
|---|---|---|
| Site down | `aws ssm describe-instance-information`, EC2 status checks (alarm `jcf-host-status-check-failed` publishes to SNS topic `jcf-alerts`; subscribe an address to it) | reboot the instance (`ec2:RebootInstances` is allowed on tagged hosts); if it does not return, replace the instance with Terraform, the data volume and Elastic IP are separate resources |
| Certificate problem | `docker logs` for caddy; DNS record must be DNS-only and point at the Elastic IP; ports 80 and 443 open | fix DNS and wait; Caddy retries |
| Core unhealthy after deploy | the deploy log prints the redacted core log tail | automatic rollback already ran; fix forward and redeploy |
| Corrupt database | `sqlite3 /var/lib/jcf/jcf.db 'PRAGMA integrity_check'` | restore from backup (above) |
| Token leaked | | rotate `JCF_API_TOKEN` (new GitHub environment secret, redeploy, update Ruxt's `NUXT_JOB_AUTOMATION_TOKEN`); worker token rotates by changing the Terraform `random_id` |
| Stored credentials unreadable | `JCF_SECRET_KEY` lost or changed | re-enter credentials in Ruxt, Platform accounts |
