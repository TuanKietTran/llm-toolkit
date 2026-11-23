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

Open items are tracked in `README.md` (status table) and the final report.
