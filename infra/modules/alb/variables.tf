variable "name" {
  description = "Name prefix for every resource."
  type        = string
}

variable "vpc_id" {
  description = "VPC the load balancer lives in."
  type        = string
}

variable "public_subnet_ids" {
  description = "Public subnets, one per AZ."
  type        = list(string)
}

variable "target_port" {
  description = "Port the server listens on."
  type        = number
  default     = 3000
}

variable "certificate_arn" {
  description = "ACM certificate. Empty serves plain HTTP, which is only acceptable in sandbox."
  type        = string
  default     = ""
}

variable "deletion_protection" {
  description = "Refuse to destroy the load balancer."
  type        = bool
  default     = false
}
