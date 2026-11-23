variable "region" {
  type    = string
  default = "us-east-1"
}

variable "hostname" {
  description = "Public hostname Caddy serves and requests a certificate for."
  type        = string
  default     = "jcf-aws.mashallow.cloud"
}

variable "instance_type" {
  description = "Only t4g.micro and t4g.small are permitted by the deploy role."
  type        = string
  default     = "t4g.small"
  validation {
    condition     = contains(["t4g.micro", "t4g.small"], var.instance_type)
    error_message = "The deploy role only allows t4g.micro or t4g.small."
  }
}

variable "root_volume_gib" {
  type    = number
  default = 16
}

variable "data_volume_gib" {
  description = "SQLite database and local backups. The deploy role caps volumes at 50 GiB."
  type        = number
  default     = 20
  validation {
    condition     = var.data_volume_gib >= 8 && var.data_volume_gib <= 50
    error_message = "data_volume_gib must be between 8 and 50."
  }
}

variable "backup_retention_days" {
  description = "Days an S3 database backup is kept before the lifecycle rule expires it."
  type        = number
  default     = 35
}

variable "api_token" {
  description = "Bearer token Ruxt's server uses for /api/v1. Supplied through TF_VAR_api_token from a GitHub environment secret; never defaulted."
  type        = string
  sensitive   = true
  validation {
    condition     = length(var.api_token) >= 24
    error_message = "api_token must be at least 24 characters."
  }
}

variable "greenhouse_boards" {
  description = "JCF_GREENHOUSE_BOARDS: comma list of token:Company[:location regex]."
  type        = string
  default     = ""
}

variable "lever_sites" {
  description = "JCF_LEVER_SITES: comma list of site:Company."
  type        = string
  default     = ""
}

variable "discover_every" {
  type    = string
  default = "6h"
}

variable "compose_version" {
  type    = string
  default = "v5.8.0"
}

variable "compose_sha256_arm64" {
  description = "sha256 of docker-compose-linux-aarch64 for compose_version (from the release checksums.txt)."
  type        = string
  default     = "1a9c918b2fb49b9c87f0018c64b5abd0d30715155e98dc939779fbaee6c414d3"
}
