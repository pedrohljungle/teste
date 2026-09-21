variable "name" {
  description = "Name prefix for every resource."
  type        = string
}

variable "environment" {
  description = "Environment name, passed to the application as APP_ENV."
  type        = string
}

variable "region" {
  description = "AWS region, for the log driver and the SDK."
  type        = string
}

variable "vpc_id" {
  description = "VPC the tasks live in."
  type        = string
}

variable "private_subnet_ids" {
  description = "Private subnets the tasks run in. They never get a public address."
  type        = list(string)
}

variable "alb_security_group_id" {
  description = "The only security group allowed to reach the tasks."
  type        = string
}

variable "target_group_arn" {
  description = "Target group the server service registers into."
  type        = string
}

variable "alb_listener_arns" {
  description = "Listeners the service waits for before registering targets."
  type        = list(string)
}

variable "container_port" {
  description = "Port both processes listen on."
  type        = number
  default     = 3000
}

variable "cpu_architecture" {
  description = "Task architecture. ARM64 matches the Graviton pricing of the rest of the stack."
  type        = string
  default     = "ARM64"
}

variable "server_image" {
  description = "Image of the HTTP server."
  type        = string
}

variable "worker_image" {
  description = "Image of the queue worker."
  type        = string
}

variable "server_cpu" {
  description = "CPU units of the server task."
  type        = number
  default     = 256
}

variable "server_memory" {
  description = "Memory of the server task in MiB."
  type        = number
  default     = 512
}

variable "server_replicas" {
  description = "Desired number of server tasks."
  type        = number
  default     = 1
}

variable "worker_cpu" {
  description = "CPU units of the worker task."
  type        = number
  default     = 256
}

variable "worker_memory" {
  description = "Memory of the worker task in MiB."
  type        = number
  default     = 512
}

variable "worker_replicas" {
  description = "Desired number of worker tasks."
  type        = number
  default     = 1
}

variable "worker_concurrency" {
  description = "Messages a single worker task processes in parallel."
  type        = number
  default     = 4
}

variable "database_url" {
  description = "Connection URL WITHOUT the password. The password is injected as a secret."
  type        = string
}

variable "database_secret_arn" {
  description = "Secrets Manager secret holding the database password."
  type        = string
}

variable "redis_url" {
  description = "Connection URL of the cache."
  type        = string
}

variable "queue_url" {
  description = "URL of the job queue."
  type        = string
}

variable "queue_arn" {
  description = "ARN of the job queue, for the task policies."
  type        = string
}

variable "dlq_url" {
  description = "URL of the dead letter queue the worker sends unprocessable messages to."
  type        = string
}

variable "dlq_arn" {
  description = "ARN of the dead letter queue, for the worker policy."
  type        = string
}

variable "events_queue_url" {
  description = "URL of the FIFO queue of integration events, which the outbox publisher sends to."
  type        = string
}

variable "events_queue_arn" {
  description = "ARN of the queue of integration events, for the worker policy."
  type        = string
}

variable "queue_visibility_timeout" {
  description = "Visibility timeout of the queue, mirrored into the worker configuration."
  type        = number
  default     = 60
}

variable "keycloak_issuer" {
  description = "External realm address, the one written into the token iss claim."
  type        = string
}

variable "keycloak_audience" {
  description = "Audience the server requires in the token."
  type        = string
}

variable "keycloak_client_id" {
  description = "Service account client the worker authenticates as."
  type        = string
}

variable "keycloak_client_secret_arn" {
  description = "Secrets Manager secret holding the worker client secret."
  type        = string
}

variable "otlp_endpoint" {
  description = "host:port of the OTLP collector. Empty turns telemetry export off."
  type        = string
  default     = ""
}

variable "trace_sample_ratio" {
  description = "Fraction of traces exported."
  type        = number
  default     = 0.1
}

variable "log_retention_days" {
  description = "Retention of the task log groups."
  type        = number
  default     = 14
}

variable "container_insights" {
  description = "Container Insights on the cluster."
  type        = bool
  default     = true
}
