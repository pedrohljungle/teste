variable "name" {
  description = "Name prefix for every resource."
  type        = string
}

variable "vpc_cidr" {
  description = "CIDR of the VPC."
  type        = string
}

variable "single_nat_gateway" {
  description = "One NAT gateway for both AZs. Cheaper, and a single point of failure for outbound traffic."
  type        = bool
  default     = false
}
