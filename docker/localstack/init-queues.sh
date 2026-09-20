#!/bin/bash
# LocalStack runs every script in ready.d once the emulator is up. Creating the queue here,
# and not from the application, keeps the app from needing permission to create its own
# infrastructure — which is exactly what it will not have in AWS.
set -euo pipefail

awslocal sqs create-queue --queue-name pedro-test-tasks-dlq

awslocal sqs create-queue \
  --queue-name pedro-test-tasks \
  --attributes "{\"RedrivePolicy\":\"{\\\"deadLetterTargetArn\\\":\\\"arn:aws:sqs:us-east-1:000000000000:pedro-test-tasks-dlq\\\",\\\"maxReceiveCount\\\":\\\"5\\\"}\",\"VisibilityTimeout\":\"60\"}"

# The integration events the outbox publisher sends, and their dead letter queue. Both are FIFO:
# the message group is the aggregate id, so the events of one aggregate reach a consumer in the
# order they were sent, and the deduplication id is the event id, so a republication after a crash
# is dropped by the queue inside its window and recognised by the consumer beyond it.
awslocal sqs create-queue --queue-name wager-events-dlq.fifo --attributes FifoQueue=true

awslocal sqs create-queue \
  --queue-name wager-events.fifo \
  --attributes "{\"FifoQueue\":\"true\",\"RedrivePolicy\":\"{\\\"deadLetterTargetArn\\\":\\\"arn:aws:sqs:us-east-1:000000000000:wager-events-dlq.fifo\\\",\\\"maxReceiveCount\\\":\\\"5\\\"}\",\"VisibilityTimeout\":\"60\"}"

echo "queues ready"
