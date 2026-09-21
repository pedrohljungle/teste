output "queue_url" {
  description = "URL of the job queue."
  value       = aws_sqs_queue.tasks.url
}

output "queue_arn" {
  description = "ARN of the job queue."
  value       = aws_sqs_queue.tasks.arn
}

output "dlq_url" {
  description = "URL of the dead letter queue, which the worker sends to what no retry can fix."
  value       = aws_sqs_queue.dlq.url
}

output "dlq_arn" {
  description = "ARN of the dead letter queue."
  value       = aws_sqs_queue.dlq.arn
}

output "visibility_timeout_seconds" {
  description = "Visibility timeout, so the task definition can be configured with the same value."
  value       = aws_sqs_queue.tasks.visibility_timeout_seconds
}

output "events_queue_url" {
  description = "URL of the FIFO queue of integration events."
  value       = aws_sqs_queue.events.url
}

output "events_queue_arn" {
  description = "ARN of the FIFO queue of integration events."
  value       = aws_sqs_queue.events.arn
}
