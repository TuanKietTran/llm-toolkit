# Execution journal

All entries are from 2026-11-23, in order. Only verified outcomes are recorded.

| # | Step | Result |
|---|---|---|
| 1 | Baseline recon (`BASELINE.md`) | Go tests pass; no AWS credentials on the workstation, so every AWS action runs in GitHub Actions through OIDC |
| 2 | Terraform, Dockerfiles, host scripts, CI and deploy workflows written | `terraform validate` and `cfn-lint` clean |
| 3 | GitHub environment `aws-prod` created (deployment branches: `main` and the bring-up branch), variables `AWS_ROLE_ARN`, `AWS_REGION`, `JCF_HOSTNAME`, board lists, secret `JCF_API_TOKEN` | Verified through the API |
| 4 | Run 1 | Trivy misconfiguration scan failed: three findings (SNS encryption, unrestricted egress, SSE-S3 instead of a customer key). Each is a justified, documented exception in `main.tf` |
| 5 | Run 3 | `e2e.sh` failed on Linux: the script exec'd a Windows venv path first. Fixed to pick the interpreter that exists. Cross-process e2e now passes in CI |
| 6 | Run (first with AWS jobs) | `sts:AssumeRoleWithWebIdentity` denied. Cause: the repository emits an immutable OIDC subject (`repo:<owner>@<id>/<repo>@<id>:environment:aws-prod`, from `GET /repos/.../actions/oidc/customization/sub`), the trust policy used the plain form |
| 7 | `jcf-bootstrap` updated in the console (change set: one in-place Modify of `DeployRole`, no replacement) with a new `GitHubSubjectPrefix` parameter | `UPDATE_COMPLETE`. Template change committed |
| 8 | Run: role assumed, `terraform plan` and apply ran | S3 backup bucket, ECR repositories, log group, SNS topic, SSM parameters, IAM roles, security group and data volume were created. `RunInstances` was denied on `volume/*`: the root volume was not tagged at launch, so the role's `aws:RequestTag/Project` condition failed. Fixed with `volume_tags` |
| 9 | Run: instance created, apply failed on the next run | The provider reads `volume_tags` back from every attached volume (including the data volume), so the plan wanted to retag the data volume and drop its `Snapshot` tag. The deploy role refused `CreateTags`, which protected the snapshot policy. Fixed with `ignore_changes = [volume_tags]` |
| 10 | DNS | `jcf.mashallow.cloud` already existed (proxied tunnel to the previous instance, answering 200) so it was not touched. Added a DNS-only `A` record `jcf-aws.mashallow.cloud` pointing at the Elastic IP |
| 11 | Images | Worker image scan: five HIGH findings in pip's vendored `urllib3` and `msgpack` and a bundled `setuptools`. pip, setuptools and wheel are now removed from the final image |
| 12 | First full pipeline | test, infra, images, deploy and verify all passed; HTTPS, redirects, private-route blocking, API auth, backup and restore verification confirmed from the workflow output |
| 13 | Production journey job added | real discovery (154 jobs from three public boards) and a core restart that kept all 154 rows; passed |
| 14 | Mistake recorded | A local filesystem path was put into `BASELINE.md` and pushed to the public branch; it was removed in the next commit but remains in the branch history (no force-push was done) |

Open items are tracked in `README.md` (status table) and the final report.
