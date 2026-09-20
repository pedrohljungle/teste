# The job queue, with its dead letter queue.
#
# It is created here and not by the application on purpose: in AWS the tasks will not have
# permission to create their own infrastructure, and it is better that the local stack has the
# same restriction (see docker/localstack/init-queues.sh).

resource "aws_sqs_queue" "dlq" {
  name = "${var.name}-tasks-dlq"

  # Two weeks, the maximum: a message that failed five times is evidence someone has to look
  # at, and it has to survive a long weekend.
  message_retention_seconds = 1209600

  tags = { Name = "${var.name}-tasks-dlq" }
}

resource "aws_sqs_queue" "tasks" {
  name = "${var.name}-tasks"

  # Must exceed the slowest handler, otherwise a message still being processed is delivered
  # again. It matches WORKER_VISIBILITY_TIMEOUT in the task definition.
  visibility_timeout_seconds = var.visibility_timeout_seconds
  message_retention_seconds  = 345600 # 4 days
  # Long polling at the queue level, so a consumer that forgets to ask for it still gets it.
  receive_wait_time_seconds = 20

  redrive_policy = jsonencode({
    deadLetterTargetArn = aws_sqs_queue.dlq.arn
    maxReceiveCount     = var.max_receive_count
  })

  tags = { Name = "${var.name}-tasks" }
}

# An alarm on the dead letter queue is the only thing that turns "the worker gave up" into
# something a person hears about.
resource "aws_cloudwatch_metric_alarm" "dlq_not_empty" {
  alarm_name          = "${var.name}-tasks-dlq-not-empty"
  alarm_description   = "Messages the worker could not process after ${var.max_receive_count} deliveries"
  namespace           = "AWS/SQS"
  metric_name         = "ApproximateNumberOfMessagesVisible"
  statistic           = "Maximum"
  period              = 300
  evaluation_periods  = 1
  threshold           = 0
  comparison_operator = "GreaterThanThreshold"
  treat_missing_data  = "notBreaching"

  dimensions = { QueueName = aws_sqs_queue.dlq.name }

  alarm_actions = var.alarm_actions
  ok_actions    = var.alarm_actions
}
