data "aws_caller_identity" "current" {}
data "aws_partition" "current" {}

data "aws_vpc" "default" {
  default = true
}

data "aws_subnets" "default" {
  filter {
    name   = "vpc-id"
    values = [data.aws_vpc.default.id]
  }
  filter {
    name   = "default-for-az"
    values = ["true"]
  }
}

data "aws_subnet" "selected" {
  id = local.subnet_id
}

data "aws_ssm_parameter" "al2023_arm64" {
  name = "/aws/service/ami-amazon-linux-latest/al2023-ami-kernel-default-arm64"
}

locals {
  account_id = data.aws_caller_identity.current.account_id
  partition  = data.aws_partition.current.partition
  tags       = { Project = "jcf", Component = "job-cv-finder", ManagedBy = "terraform" }
  # The boundary policy is created by the jcf-bootstrap CloudFormation stack.
  boundary_arn = "arn:${local.partition}:iam::${local.account_id}:policy/jcf-boundary"
  bucket       = "jcf-backup-${local.account_id}-${var.region}"
  subnet_id    = sort(data.aws_subnets.default.ids)[0]
}

# ----------------------------------------------------------------------------- registry
resource "aws_ecr_repository" "app" {
  for_each             = toset(["core", "intel"])
  name                 = "jcf-${each.key}"
  image_tag_mutability = "IMMUTABLE"
  force_delete         = false
  tags                 = local.tags

  image_scanning_configuration {
    scan_on_push = true
  }
}

resource "aws_ecr_lifecycle_policy" "app" {
  for_each   = aws_ecr_repository.app
  repository = each.value.name
  policy = jsonencode({
    rules = [{
      rulePriority = 1
      description  = "Keep the 15 newest images"
      selection    = { tagStatus = "any", countType = "imageCountMoreThan", countNumber = 15 }
      action       = { type = "expire" }
    }]
  })
}

# ----------------------------------------------------------------------------- logs, alerts
resource "aws_cloudwatch_log_group" "app" {
  name              = "/jcf/app"
  retention_in_days = 30
  tags              = local.tags
}

# CloudWatch cannot publish to a topic encrypted with the AWS-managed key and the
# deploy role cannot create customer-managed keys; the topic only carries alarm state, no personal data.
#trivy:ignore:AWS-0095
resource "aws_sns_topic" "alerts" {
  name = "jcf-alerts"
  tags = local.tags
}

# ----------------------------------------------------------------------------- backup bucket
resource "aws_s3_bucket" "backup" {
  bucket = local.bucket
  tags   = local.tags

  lifecycle {
    prevent_destroy = true
  }
}

resource "aws_s3_bucket_ownership_controls" "backup" {
  bucket = aws_s3_bucket.backup.id
  rule {
    object_ownership = "BucketOwnerEnforced"
  }
}

resource "aws_s3_bucket_public_access_block" "backup" {
  bucket                  = aws_s3_bucket.backup.id
  block_public_acls       = true
  block_public_policy     = true
  ignore_public_acls      = true
  restrict_public_buckets = true
}

resource "aws_s3_bucket_versioning" "backup" {
  bucket = aws_s3_bucket.backup.id
  versioning_configuration {
    status = "Enabled"
  }
}

# SSE-S3 (AES256) on purpose: the deploy role cannot create customer-managed KMS keys.
#trivy:ignore:AWS-0132
resource "aws_s3_bucket_server_side_encryption_configuration" "backup" {
  bucket = aws_s3_bucket.backup.id
  rule {
    apply_server_side_encryption_by_default {
      sse_algorithm = "AES256"
    }
  }
}

resource "aws_s3_bucket_lifecycle_configuration" "backup" {
  bucket = aws_s3_bucket.backup.id
  rule {
    id     = "expire-old-backups"
    status = "Enabled"
    filter {}
    expiration {
      days = var.backup_retention_days
    }
    noncurrent_version_expiration {
      noncurrent_days = 7
    }
    abort_incomplete_multipart_upload {
      days_after_initiation = 2
    }
  }
}

resource "aws_s3_bucket_policy" "backup" {
  bucket = aws_s3_bucket.backup.id
  policy = jsonencode({
    Version = "2012-10-17"
    Statement = [{
      Sid       = "DenyInsecureTransport"
      Effect    = "Deny"
      Principal = "*"
      Action    = "s3:*"
      Resource  = [aws_s3_bucket.backup.arn, "${aws_s3_bucket.backup.arn}/*"]
      Condition = { Bool = { "aws:SecureTransport" = "false" } }
    }]
  })
  depends_on = [aws_s3_bucket_public_access_block.backup]
}

# ----------------------------------------------------------------------------- secrets (SSM)
# Generated once and kept in state (encrypted bucket, role-restricted) and in SSM. JCF_SECRET_KEY
# encrypts stored platform credentials: keep this parameter, losing it means re-entering them.
resource "random_id" "worker_token" {
  byte_length = 32
}

resource "random_id" "secret_key" {
  byte_length = 32
}

locals {
  secure_param_names = toset(["JCF_API_TOKEN", "JCF_WORKER_TOKEN", "JCF_SECRET_KEY"])
  plain_params = merge(
    {
      JCF_HOSTNAME       = var.hostname
      JCF_DISCOVER_EVERY = var.discover_every
    },
    var.greenhouse_boards == "" ? {} : { JCF_GREENHOUSE_BOARDS = var.greenhouse_boards },
    var.lever_sites == "" ? {} : { JCF_LEVER_SITES = var.lever_sites },
  )
}

resource "aws_ssm_parameter" "secure" {
  for_each = local.secure_param_names
  name     = "/jcf/env/${each.key}"
  type     = "SecureString"
  value = {
    JCF_API_TOKEN    = var.api_token
    JCF_WORKER_TOKEN = random_id.worker_token.b64_url
    JCF_SECRET_KEY   = random_id.secret_key.b64_std
  }[each.key]
  tags = local.tags
}

resource "aws_ssm_parameter" "plain" {
  for_each = local.plain_params
  name     = "/jcf/env/${each.key}"
  type     = "String"
  value    = each.value
  tags     = local.tags
}

# ----------------------------------------------------------------------------- IAM (boundary required)
data "aws_iam_policy_document" "ec2_trust" {
  statement {
    actions = ["sts:AssumeRole"]
    principals {
      type        = "Service"
      identifiers = ["ec2.amazonaws.com"]
    }
  }
}

data "aws_iam_policy_document" "dlm_trust" {
  statement {
    actions = ["sts:AssumeRole"]
    principals {
      type        = "Service"
      identifiers = ["dlm.amazonaws.com"]
    }
  }
}

resource "aws_iam_role" "host" {
  name                 = "jcf-host"
  assume_role_policy   = data.aws_iam_policy_document.ec2_trust.json
  permissions_boundary = local.boundary_arn
  tags                 = local.tags
}

# Inline (not managed-policy attachments): the deploy role is not allowed to attach policies.
data "aws_iam_policy_document" "host" {
  statement {
    sid = "SsmAgent"
    actions = [
      "ssm:UpdateInstanceInformation", "ssm:DescribeAssociation", "ssm:GetDocument",
      "ssm:ListAssociations", "ssm:ListInstanceAssociations", "ssm:GetDeployablePatchSnapshotForInstance",
      "ssm:UpdateAssociationStatus", "ssm:UpdateInstanceAssociationStatus", "ssm:PutInventory",
      "ssmmessages:CreateControlChannel", "ssmmessages:CreateDataChannel",
      "ssmmessages:OpenControlChannel", "ssmmessages:OpenDataChannel",
      "ec2messages:AcknowledgeMessage", "ec2messages:DeleteMessage", "ec2messages:FailMessage",
      "ec2messages:GetEndpoint", "ec2messages:GetMessages", "ec2messages:SendReply",
    ]
    resources = ["*"]
  }
  statement {
    sid       = "ReadOwnParameters"
    actions   = ["ssm:GetParameter", "ssm:GetParameters", "ssm:GetParametersByPath"]
    resources = ["arn:${local.partition}:ssm:${var.region}:${local.account_id}:parameter/jcf/*"]
  }
  statement {
    sid       = "DecryptParametersViaSsm"
    actions   = ["kms:Decrypt"]
    resources = ["arn:${local.partition}:kms:${var.region}:${local.account_id}:key/*"]
    condition {
      test     = "StringEquals"
      variable = "kms:ViaService"
      values   = ["ssm.${var.region}.amazonaws.com"]
    }
  }
  statement {
    sid       = "EcrLogin"
    actions   = ["ecr:GetAuthorizationToken"]
    resources = ["*"]
  }
  statement {
    sid       = "EcrPull"
    actions   = ["ecr:BatchCheckLayerAvailability", "ecr:GetDownloadUrlForLayer", "ecr:BatchGetImage"]
    resources = [for r in aws_ecr_repository.app : r.arn]
  }
  statement {
    sid       = "Logs"
    actions   = ["logs:CreateLogStream", "logs:PutLogEvents", "logs:DescribeLogStreams"]
    resources = [aws_cloudwatch_log_group.app.arn, "${aws_cloudwatch_log_group.app.arn}:*"]
  }
  statement {
    sid       = "BackupsWrite"
    actions   = ["s3:PutObject", "s3:GetObject", "s3:AbortMultipartUpload"]
    resources = ["${aws_s3_bucket.backup.arn}/*"]
  }
  statement {
    sid       = "BackupsList"
    actions   = ["s3:ListBucket", "s3:GetBucketLocation"]
    resources = [aws_s3_bucket.backup.arn]
  }
}

resource "aws_iam_role_policy" "host" {
  name   = "jcf-host"
  role   = aws_iam_role.host.id
  policy = data.aws_iam_policy_document.host.json
}

resource "aws_iam_instance_profile" "host" {
  name = "jcf-host"
  role = aws_iam_role.host.name
  tags = local.tags
}

resource "aws_iam_role" "dlm" {
  name                 = "jcf-dlm"
  assume_role_policy   = data.aws_iam_policy_document.dlm_trust.json
  permissions_boundary = local.boundary_arn
  tags                 = local.tags
}

data "aws_iam_policy_document" "dlm" {
  statement {
    sid       = "Snapshots"
    actions   = ["ec2:CreateSnapshot", "ec2:CreateSnapshots", "ec2:DeleteSnapshot", "ec2:CreateTags"]
    resources = ["arn:${local.partition}:ec2:${var.region}::snapshot/*", "arn:${local.partition}:ec2:${var.region}:${local.account_id}:volume/*"]
  }
  statement {
    sid       = "Describe"
    actions   = ["ec2:DescribeInstances", "ec2:DescribeVolumes", "ec2:DescribeSnapshots"]
    resources = ["*"]
  }
}

resource "aws_iam_role_policy" "dlm" {
  name   = "jcf-dlm"
  role   = aws_iam_role.dlm.id
  policy = data.aws_iam_policy_document.dlm.json
}

# ----------------------------------------------------------------------------- network
# the host must reach ECR, SSM, S3, Let's Encrypt and arbitrary public job boards, so
# outbound cannot be limited to a fixed CIDR list. Inbound is limited to 80/443.
#trivy:ignore:AWS-0104
resource "aws_security_group" "host" {
  name        = "jcf-host"
  description = "Job CV Finder host: HTTP and HTTPS only. No SSH, management is through SSM."
  vpc_id      = data.aws_vpc.default.id
  tags        = merge(local.tags, { Name = "jcf-host" })

  # Inline rules on purpose: tagging separate rule resources is not permitted for the deploy role.
  ingress {
    description = "HTTP (ACME HTTP-01 and redirect to HTTPS)"
    from_port   = 80
    to_port     = 80
    protocol    = "tcp"
    cidr_blocks = ["0.0.0.0/0"]
  }
  ingress {
    description = "HTTPS"
    from_port   = 443
    to_port     = 443
    protocol    = "tcp"
    cidr_blocks = ["0.0.0.0/0"]
  }
  ingress {
    description = "HTTP/3"
    from_port   = 443
    to_port     = 443
    protocol    = "udp"
    cidr_blocks = ["0.0.0.0/0"]
  }
  #trivy:ignore:AWS-0104
  egress {
    description = "Outbound (ECR, SSM, S3, ACME, job boards)"
    from_port   = 0
    to_port     = 0
    protocol    = "-1"
    cidr_blocks = ["0.0.0.0/0"]
  }
}

# ----------------------------------------------------------------------------- compute and storage
resource "aws_ebs_volume" "data" {
  availability_zone = data.aws_subnet.selected.availability_zone
  size              = var.data_volume_gib
  type              = "gp3"
  encrypted         = true
  tags              = merge(local.tags, { Name = "jcf-data", Snapshot = "jcf-daily" })

  lifecycle {
    prevent_destroy = true
  }
}

resource "aws_instance" "host" {
  ami                         = data.aws_ssm_parameter.al2023_arm64.value
  instance_type               = var.instance_type
  subnet_id                   = local.subnet_id
  vpc_security_group_ids      = [aws_security_group.host.id]
  iam_instance_profile        = aws_iam_instance_profile.host.name
  associate_public_ip_address = true
  monitoring                  = false
  user_data = templatefile("${path.module}/user_data.sh.tpl", {
    region               = var.region
    bucket               = local.bucket
    data_volume_id       = replace(aws_ebs_volume.data.id, "-", "")
    compose_version      = var.compose_version
    compose_sha256_arm64 = var.compose_sha256_arm64
  })

  # Burstable credits stay capped instead of "unlimited" (which bills for surplus CPU).
  credit_specification {
    cpu_credits = "standard"
  }

  metadata_options {
    http_endpoint               = "enabled"
    http_tokens                 = "required"
    http_put_response_hop_limit = 2
  }

  root_block_device {
    volume_size = var.root_volume_gib
    volume_type = "gp3"
    encrypted   = true
  }

  # Applied at instance-creation time (RunInstances tag specification for the volume); the deploy role
  # only allows creating volumes that carry Project=jcf at that moment.
  volume_tags = merge(local.tags, { Name = "jcf-root" })
  tags        = merge(local.tags, { Name = "jcf-host" })

  lifecycle {
    # The AMI alias moves forward over time and user data is applied on first boot only;
    # neither may replace a host that holds production data. volume_tags only matters at launch: the
    # provider reads it back from every attached volume (including the data volume, which has its own
    # tags), so leaving it managed would retag the data volume and drop its snapshot-policy tag.
    ignore_changes = [ami, user_data, volume_tags]
  }
}

resource "aws_volume_attachment" "data" {
  device_name = "/dev/sdf"
  volume_id   = aws_ebs_volume.data.id
  instance_id = aws_instance.host.id
}

resource "aws_eip" "host" {
  domain   = "vpc"
  instance = aws_instance.host.id
  tags     = merge(local.tags, { Name = "jcf-host" })
}

# ----------------------------------------------------------------------------- snapshots and alarms
resource "aws_dlm_lifecycle_policy" "data" {
  description        = "Daily snapshots of the jcf data volume 14 kept"
  execution_role_arn = aws_iam_role.dlm.arn
  state              = "ENABLED"
  tags               = local.tags

  policy_details {
    resource_types = ["VOLUME"]
    target_tags    = { Snapshot = "jcf-daily" }

    schedule {
      name = "daily"
      create_rule {
        interval      = 24
        interval_unit = "HOURS"
        times         = ["03:30"]
      }
      retain_rule {
        count = 14
      }
      copy_tags = true
      tags_to_add = {
        Project = "jcf"
      }
    }
  }
}

resource "aws_cloudwatch_metric_alarm" "status_check" {
  alarm_name          = "jcf-host-status-check-failed"
  alarm_description   = "The jcf host failed an EC2 status check for 3 minutes (a stopped host is not an alarm: the host is on demand)."
  namespace           = "AWS/EC2"
  metric_name         = "StatusCheckFailed"
  dimensions          = { InstanceId = aws_instance.host.id }
  statistic           = "Maximum"
  period              = 60
  evaluation_periods  = 3
  threshold           = 1
  comparison_operator = "GreaterThanOrEqualToThreshold"
  treat_missing_data  = "notBreaching"
  alarm_actions       = [aws_sns_topic.alerts.arn]
  ok_actions          = [aws_sns_topic.alerts.arn]
  tags                = local.tags
}
