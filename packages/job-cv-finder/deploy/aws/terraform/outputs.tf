output "instance_id" {
  value = aws_instance.host.id
}

output "public_ip" {
  description = "Elastic IP. Create a DNS-only A record for the hostname pointing here."
  value       = aws_eip.host.public_ip
}

output "hostname" {
  value = var.hostname
}

output "backup_bucket" {
  value = aws_s3_bucket.backup.id
}

output "ecr_core" {
  value = aws_ecr_repository.app["core"].repository_url
}

output "ecr_intel" {
  value = aws_ecr_repository.app["intel"].repository_url
}

output "alert_topic_arn" {
  value = aws_sns_topic.alerts.arn
}
