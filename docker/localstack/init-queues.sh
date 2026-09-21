#!/bin/bash
# LocalStack runs every script in ready.d once the emulator is up. Creating the queue here,
# and not from the application, keeps the app from needing permission to create its own
# infrastructure — which is exactly what it will not have in AWS.
set -euo pipefail

# The operations the providers send, and their dead letter queue. Both are FIFO. The producer sets
# the message group to the wallet id, so the operations of one wallet are consumed one at a time and
# in order while different wallets go in parallel, and the deduplication id to the idempotency key,
# so the queue drops a resend inside its five minute window. Beyond that window, and whenever the
# producer chooses another id, the inbox and the unique constraints are what guarantee an operation
# is applied once: the queue is an optimisation of that, not the guarantee.
awslocal sqs create-queue --queue-name wager-transactions-dlq.fifo --attributes FifoQueue=true

awslocal sqs create-queue \
  --queue-name wager-transactions.fifo \
  --attributes "{\"FifoQueue\":\"true\",\"RedrivePolicy\":\"{\\\"deadLetterTargetArn\\\":\\\"arn:aws:sqs:us-east-1:000000000000:wager-transactions-dlq.fifo\\\",\\\"maxReceiveCount\\\":\\\"5\\\"}\",\"VisibilityTimeout\":\"60\"}"

# The integration events the outbox publisher sends, and their dead letter queue. Both are FIFO:
# the message group is the aggregate id, so the events of one aggregate reach a consumer in the
# order they were sent, and the deduplication id is the event id, so a republication after a crash
# is dropped by the queue inside its window and recognised by the consumer beyond it.
awslocal sqs create-queue --queue-name wager-events-dlq.fifo --attributes FifoQueue=true

awslocal sqs create-queue \
  --queue-name wager-events.fifo \
  --attributes "{\"FifoQueue\":\"true\",\"RedrivePolicy\":\"{\\\"deadLetterTargetArn\\\":\\\"arn:aws:sqs:us-east-1:000000000000:wager-events-dlq.fifo\\\",\\\"maxReceiveCount\\\":\\\"5\\\"}\",\"VisibilityTimeout\":\"60\"}"

echo "queues ready"
