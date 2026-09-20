#!/bin/bash
# LocalStack runs every script in ready.d once the emulator is up. Creating the queue here,
# and not from the application, keeps the app from needing permission to create its own
# infrastructure — which is exactly what it will not have in AWS.
set -euo pipefail

awslocal sqs create-queue --queue-name pedro-test-tasks-dlq

awslocal sqs create-queue \
  --queue-name pedro-test-tasks \
  --attributes "{\"RedrivePolicy\":\"{\\\"deadLetterTargetArn\\\":\\\"arn:aws:sqs:us-east-1:000000000000:pedro-test-tasks-dlq\\\",\\\"maxReceiveCount\\\":\\\"5\\\"}\",\"VisibilityTimeout\":\"60\"}"

echo "queues ready"
