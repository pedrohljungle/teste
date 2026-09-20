variable "name" {
  description = "Name prefix for every resource."
  type        = string
}

variable "visibility_timeout_seconds" {
  description = "How long a delivered message stays hidden. Must exceed the slowest handler."
  type        = number
  default     = 60
}

variable "max_receive_count" {
  description = "Deliveries before a message goes to the dead letter queue."
  type        = number
  default     = 5
}

variable "alarm_actions" {
  description = "SNS topics notified when the dead letter queue is not empty."
  type        = list(string)
  default     = []
}
