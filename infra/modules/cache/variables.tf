variable "name" {
  description = "Name prefix for every resource."
  type        = string
}

variable "vpc_id" {
  description = "VPC the cache lives in."
  type        = string
}

variable "private_subnet_ids" {
  description = "Private subnets, one per AZ."
  type        = list(string)
}

variable "engine_version" {
  description = "Redis version."
  type        = string
  default     = "7.1"
}

variable "node_type" {
  description = "Node type. Graviton (t4g) by default."
  type        = string
  default     = "cache.t4g.micro"
}

variable "node_count" {
  description = "Number of nodes. More than one enables automatic failover across AZs."
  type        = number
  default     = 1
}

variable "apply_immediately" {
  description = "Apply changes without waiting for the maintenance window."
  type        = bool
  default     = false
}
