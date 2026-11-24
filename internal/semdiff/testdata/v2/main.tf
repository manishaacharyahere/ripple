terraform {
  required_version = ">= 1.5"
  required_providers {
    aws = { source = "hashicorp/aws", version = ">= 5.0" }
  }
}

variable "name" { type = string }
variable "tags" {
  type    = map( string )
  default = {}
}
variable "versioning" {
  type    = bool
  default = true
}
variable "kms_key" { type = string }
variable "lifecycle_days" {
  type    = number
  default = 30
}
variable "owner" { type = string }

resource "aws_s3_bucket" "this" { bucket = var.name }
resource "aws_s3_bucket_policy" "main" { bucket = aws_s3_bucket.this.id }
resource "aws_s3_bucket_lifecycle_configuration" "this" { bucket = aws_s3_bucket.this.id }

moved {
  from = aws_s3_bucket_policy.old
  to   = aws_s3_bucket_policy.main
}

removed {
  from = aws_s3_bucket_logging.this
  lifecycle { destroy = false }
}

module "labels" {
  source  = "cloudposse/label/null"
  version = "0.26.0"
}

output "id" { value = aws_s3_bucket.this.id }
output "arn" {
  value     = aws_s3_bucket.this.arn
  sensitive = true
}
output "region" { value = aws_s3_bucket.this.region }
