# The queue of operations, with its dead letter queue, and the queue of integration events.
#
# It is created here and not by the application on purpose: in AWS the tasks will not have
# permission to create their own infrastructure, and it is better that the local stack has the
# same restriction (see docker/localstack/init-queues.sh).

resource "aws_sqs_queue" "dlq" {
  name       = "${var.name}-wager-transactions-dlq.fifo"
  fifo_queue = true

  # Two weeks, the maximum: a message that failed five times, or that the worker gave up on, is
  # evidence someone has to look at, and it has to survive a long weekend.
  message_retention_seconds = 1209600

  tags = { Name = "${var.name}-wager-transactions-dlq" }
}

# The operations the providers send. FIFO: the producer sets the message group to the wallet id, so
# one wallet is consumed one message at a time and in order while different wallets go in
# parallel, and the deduplication id to the idempotency key. The queue's deduplication is an
# optimisation of the idempotency the database guarantees, never the guarantee itself.
resource "aws_sqs_queue" "tasks" {
  name       = "${var.name}-wager-transactions.fifo"
  fifo_queue = true

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

  tags = { Name = "${var.name}-wager-transactions" }
}

# An alarm on the dead letter queue is the only thing that turns "the worker gave up" into
# something a person hears about.
resource "aws_cloudwatch_metric_alarm" "dlq_not_empty" {
  alarm_name          = "${var.name}-wager-transactions-dlq-not-empty"
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

# The integration events the outbox publisher sends, and their dead letter queue. Both are FIFO:
# the message group is the aggregate id, so the events of one aggregate reach a consumer in the
# order they were sent, and the deduplication id is the event id, so a republication after a
# crash is dropped by the queue inside its five minute window and recognised by the consumer
# beyond it. Consumers of this queue belong to other systems; what they read is documented in
# ARCHITECTURE.md.
resource "aws_sqs_queue" "events_dlq" {
  name       = "${var.name}-wager-events-dlq.fifo"
  fifo_queue = true

  message_retention_seconds = 1209600

  tags = { Name = "${var.name}-wager-events-dlq" }
}

resource "aws_sqs_queue" "events" {
  name       = "${var.name}-wager-events.fifo"
  fifo_queue = true

  visibility_timeout_seconds = var.visibility_timeout_seconds
  message_retention_seconds  = 345600 # 4 days
  receive_wait_time_seconds  = 20

  redrive_policy = jsonencode({
    deadLetterTargetArn = aws_sqs_queue.events_dlq.arn
    maxReceiveCount     = var.max_receive_count
  })

  tags = { Name = "${var.name}-wager-events" }
}

resource "aws_cloudwatch_metric_alarm" "events_dlq_not_empty" {
  alarm_name          = "${var.name}-wager-events-dlq-not-empty"
  alarm_description   = "Integration events a consumer could not process after ${var.max_receive_count} deliveries"
  namespace           = "AWS/SQS"
  metric_name         = "ApproximateNumberOfMessagesVisible"
  statistic           = "Maximum"
  period              = 300
  evaluation_periods  = 1
  threshold           = 0
  comparison_operator = "GreaterThanThreshold"
  treat_missing_data  = "notBreaching"

  dimensions = { QueueName = aws_sqs_queue.events_dlq.name }

  alarm_actions = var.alarm_actions
  ok_actions    = var.alarm_actions
}
