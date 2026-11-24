terraform {
  required_version = ">= 1.5"
  required_providers {
    aws = { source = "hashicorp/aws", version = ">= 4.0" }
  }
}

variable "name" { type = string }
variable "tags" {
  type    = map(string)
  default = {}
}
variable "versioning" {
  type    = bool
  default = false
}
variable "legacy_acl" {
  type    = string
  default = "private"
}
variable "kms_key" {
  type    = string
  default = null
}

resource "aws_s3_bucket" "this" { bucket = var.name }
resource "aws_s3_bucket_acl" "this" { bucket = aws_s3_bucket.this.id }
resource "aws_s3_bucket_policy" "old" { bucket = aws_s3_bucket.this.id }
resource "aws_s3_bucket_logging" "this" { bucket = aws_s3_bucket.this.id }

module "labels" {
  source  = "cloudposse/label/null"
  version = "0.25.0"
}

output "id" { value = aws_s3_bucket.this.id }
output "arn" { value = aws_s3_bucket.this.arn }
output "domain" { value = aws_s3_bucket.this.bucket_domain_name }
