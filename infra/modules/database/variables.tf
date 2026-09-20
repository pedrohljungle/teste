variable "name" {
  description = "Name prefix for every resource."
  type        = string
}

variable "vpc_id" {
  description = "VPC the instance lives in."
  type        = string
}

variable "private_subnet_ids" {
  description = "Private subnets, one per AZ."
  type        = list(string)
}

variable "engine_version" {
  description = "Postgres major version."
  type        = string
  default     = "17.5"
}

variable "instance_class" {
  description = "Instance class. Graviton (t4g) by default."
  type        = string
  default     = "db.t4g.micro"
}

variable "storage_gb" {
  description = "Allocated storage in GB. Autoscaling goes up to four times this."
  type        = number
  default     = 20
}

variable "multi_az" {
  description = "Standby in the second AZ."
  type        = bool
  default     = false
}

variable "backup_days" {
  description = "Backup retention in days."
  type        = number
  default     = 7
}

variable "deletion_protection" {
  description = "Refuse to destroy the instance, and take a final snapshot when it is destroyed."
  type        = bool
  default     = false
}

variable "database_name" {
  description = "Name of the database created on the instance."
  type        = string
  default     = "pedro_test"
}

variable "master_username" {
  description = "Master user."
  type        = string
  default     = "postgres"
}
