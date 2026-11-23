terraform {
  required_version = ">= 1.10"

  required_providers {
    aws = {
      source  = "hashicorp/aws"
      version = "~> 6.73"
    }
    random = {
      source  = "hashicorp/random"
      version = "~> 3.9"
    }
  }

  # The state bucket belongs to the account's lab-bootstrap stack; this project only uses its
  # llm-toolkit/ prefix. The bucket name contains the account id, so it is passed at init time:
  #   terraform init -backend-config="bucket=lab-tfstate-<account>-us-east-1"
  backend "s3" {
    key          = "llm-toolkit/job-cv-finder/terraform.tfstate"
    region       = "us-east-1"
    encrypt      = true
    use_lockfile = true
  }
}

# Deliberately no provider-level default_tags: the deploy role may only tag on create for a few
# resource types, and tagged security-group rules would be denied. Tags are set per resource.
provider "aws" {
  region = var.region
}
