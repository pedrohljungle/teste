variable "project" {
  description = "Name prefix for every resource."
  type        = string
  default     = "pedro-test"
}

variable "region" {
  description = "AWS region."
  type        = string
  default     = "us-east-1"
}

variable "vpc_cidr" {
  description = "CIDR of the VPC. Must not overlap with any network this one peers with."
  type        = string
  default     = "10.40.0.0/16"
}

variable "server_image" {
  description = "Image of the HTTP server, including the tag. Both processes are built from the same repository with a different entrypoint."
  type        = string
}

variable "worker_image" {
  description = "Image of the queue worker, including the tag."
  type        = string
}

variable "keycloak_issuer" {
  description = "External realm address, the one written into the token iss claim."
  type        = string
}

variable "keycloak_audience" {
  description = "Audience the server requires in the token."
  type        = string
  default     = "pedro-test-api"
}

variable "otlp_endpoint" {
  description = "host:port of the OTLP gRPC collector. Empty turns telemetry export off."
  type        = string
  default     = ""
}

variable "container_port" {
  description = "Port both processes listen on."
  type        = number
  default     = 3000
}

variable "certificate_arn" {
  description = "ACM certificate for the load balancer. Empty serves plain HTTP, which is only acceptable in sandbox."
  type        = string
  default     = ""
}

variable "keycloak_client_id" {
  description = "Service account client the worker authenticates as."
  type        = string
  default     = "pedro-test-worker"
}

variable "keycloak_client_secret_arn" {
  description = "Secrets Manager secret holding the worker client secret. Created out of band, so the value never passes through Terraform."
  type        = string
}
