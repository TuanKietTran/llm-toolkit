# Known limitations and external dependencies

Honest list as of the deployment. Items marked **external** need something only the owner can provide.

## Deployment

- **Production Ruxt stays on the previous (home-hosted) core, by the owner's decision.** The AWS core answers on `https://jcf-aws.mashallow.cloud` and passes its HTTP contract checks with the owner's API token, but Ruxt's `NUXT_JOB_AUTOMATION_URL/TOKEN` secrets were not switched, because the existing instance is live, holds state the owner uses (discovered jobs, matches, a mock ATS account) and switching redeploys production Ruxt. See `docs/ops/RUNBOOK.md`, "Cut Ruxt over to the AWS core". **external: owner decision.**
- `jcf.mashallow.cloud` (the preferred name) is an existing proxied tunnel record for that instance and was left alone. The AWS host uses `jcf-aws.mashallow.cloud`.
- **The host is on demand.** It is stopped by default, so the public name does not answer until it is started (`jcf-power.yml`), and no backups are taken while it is stopped.
- Single host, single availability zone. A host or AZ loss means recovery from the latest snapshot or S3 backup (minutes to an hour), not failover. By design for one candidate (ADR-0002).
- The CloudWatch alarm publishes to an SNS topic with no subscriber. **external: subscribe an address** (not done to avoid putting a personal address in a public repository).
- The alert topic and the backup bucket use AWS-managed encryption (the deploy role cannot create KMS keys); Trivy findings for these are documented exceptions in `main.tf`.
- The deploy role can only be changed in the `jcf-bootstrap` CloudFormation stack, through the console, by the account owner. The one change needed while deploying (trust policy for the repository's immutable OIDC subject) was made there; the permission issues found afterwards were fixed in Terraform rather than by widening the role.
- No model endpoint is configured on the host; documents are produced by the evidence-bound template path.

## Product (unchanged from the README status table)

- Automated submission exists only for the mock ATS. LinkedIn, ITviec, CareerViet and VietnamWorks are discovery-by-email or pasted jobs with owner-performed submission, by policy (ADR-0005). Greenhouse and Lever are discovery-only for the configured public boards.
- Mail fetching (IMAP/Gmail API) and the retention pruning job are not implemented.
- Ruxt keeps profiles and CVs browser-local; the core receives owner-selected snapshots (ADR-0003). ADR-0003 recorded that Ruxt had no server read API to synchronize from; the later Ruxt cloud-sync feature was not evaluated as an alternative source.
