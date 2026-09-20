# Both processes run as Fargate tasks in the PRIVATE subnets, with no public address. The
# server is reachable only through the load balancer; the worker is reachable by nothing.

resource "aws_ecs_cluster" "this" {
  name = var.name

  setting {
    name  = "containerInsights"
    value = var.container_insights ? "enabled" : "disabled"
  }

  tags = { Name = var.name }
}

resource "aws_cloudwatch_log_group" "server" {
  name              = "/ecs/${var.name}-server"
  retention_in_days = var.log_retention_days
}

resource "aws_cloudwatch_log_group" "worker" {
  name              = "/ecs/${var.name}-worker"
  retention_in_days = var.log_retention_days
}

# --- security ------------------------------------------------------------------------------

resource "aws_security_group" "tasks" {
  name        = "${var.name}-tasks"
  description = "Application tasks, private only"
  vpc_id      = var.vpc_id

  tags = { Name = "${var.name}-tasks" }
}

# The only inbound rule names the load balancer security group. Nothing else in the VPC — not
# another service, not a bastion — can open a connection to a task.
resource "aws_vpc_security_group_ingress_rule" "from_alb" {
  security_group_id            = aws_security_group.tasks.id
  description                  = "HTTP from the load balancer"
  referenced_security_group_id = var.alb_security_group_id
  from_port                    = var.container_port
  to_port                      = var.container_port
  ip_protocol                  = "tcp"
}

# Outbound is open because the tasks have to reach ECR, Secrets Manager, SQS, the IDP and the
# telemetry collector. It goes through NAT, so it is egress only.
resource "aws_vpc_security_group_egress_rule" "outbound" {
  security_group_id = aws_security_group.tasks.id
  description       = "Outbound through NAT"
  cidr_ipv4         = "0.0.0.0/0"
  ip_protocol       = "-1"
}

# --- roles ---------------------------------------------------------------------------------

data "aws_iam_policy_document" "assume" {
  statement {
    actions = ["sts:AssumeRole"]
    principals {
      type        = "Service"
      identifiers = ["ecs-tasks.amazonaws.com"]
    }
  }
}

# The execution role belongs to the ECS agent: pulling the image, writing logs and resolving
# the secrets it injects. It is not the role the application code runs with.
resource "aws_iam_role" "execution" {
  name               = "${var.name}-execution"
  assume_role_policy = data.aws_iam_policy_document.assume.json
}

resource "aws_iam_role_policy_attachment" "execution" {
  role       = aws_iam_role.execution.name
  policy_arn = "arn:aws:iam::aws:policy/service-role/AmazonECSTaskExecutionRolePolicy"
}

data "aws_iam_policy_document" "read_secrets" {
  statement {
    actions   = ["secretsmanager:GetSecretValue"]
    resources = [var.database_secret_arn]
  }
}

resource "aws_iam_role_policy" "execution_secrets" {
  name   = "${var.name}-execution-secrets"
  role   = aws_iam_role.execution.id
  policy = data.aws_iam_policy_document.read_secrets.json
}

# The task role is what the application code itself can do. The server only publishes; the
# worker consumes and deletes. They are separate roles so neither can do the other's job.
resource "aws_iam_role" "server" {
  name               = "${var.name}-server"
  assume_role_policy = data.aws_iam_policy_document.assume.json
}

resource "aws_iam_role" "worker" {
  name               = "${var.name}-worker"
  assume_role_policy = data.aws_iam_policy_document.assume.json
}

data "aws_iam_policy_document" "publish" {
  statement {
    actions   = ["sqs:SendMessage", "sqs:GetQueueAttributes", "sqs:GetQueueUrl"]
    resources = [var.queue_arn]
  }
}

data "aws_iam_policy_document" "consume" {
  statement {
    actions = [
      "sqs:ReceiveMessage",
      "sqs:DeleteMessage",
      "sqs:ChangeMessageVisibility",
      "sqs:GetQueueAttributes",
      "sqs:GetQueueUrl",
    ]
    resources = [var.queue_arn]
  }
}

resource "aws_iam_role_policy" "server_publish" {
  name   = "${var.name}-server-publish"
  role   = aws_iam_role.server.id
  policy = data.aws_iam_policy_document.publish.json
}

resource "aws_iam_role_policy" "worker_consume" {
  name   = "${var.name}-worker-consume"
  role   = aws_iam_role.worker.id
  policy = data.aws_iam_policy_document.consume.json
}

# --- tasks ---------------------------------------------------------------------------------

locals {
  # Shared environment. The two processes read the same configuration and use the slice each
  # one needs, exactly as they do in docker-compose.
  common_environment = [
    { name = "APP_ENV", value = var.environment },
    { name = "PORT", value = tostring(var.container_port) },
    { name = "DATABASE_URL", value = var.database_url },
    { name = "REDIS_URL", value = var.redis_url },
    { name = "KEYCLOAK_ISSUER", value = var.keycloak_issuer },
    { name = "KEYCLOAK_AUDIENCE", value = var.keycloak_audience },
    { name = "AWS_REGION", value = var.region },
    { name = "SQS_QUEUE_URL", value = var.queue_url },
    { name = "OTEL_EXPORTER_OTLP_ENDPOINT", value = var.otlp_endpoint },
    { name = "OTEL_TRACES_SAMPLER_ARG", value = tostring(var.trace_sample_ratio) },
  ]

  # The password never appears in an environment variable and never reaches Terraform state:
  # the ECS agent resolves it from Secrets Manager at start.
  database_secret = [
    { name = "DATABASE_PASSWORD", valueFrom = "${var.database_secret_arn}:password::" },
  ]
}

resource "aws_ecs_task_definition" "server" {
  family                   = "${var.name}-server"
  requires_compatibilities = ["FARGATE"]
  network_mode             = "awsvpc"
  cpu                      = var.server_cpu
  memory                   = var.server_memory
  execution_role_arn       = aws_iam_role.execution.arn
  task_role_arn            = aws_iam_role.server.arn

  runtime_platform {
    operating_system_family = "LINUX"
    cpu_architecture        = var.cpu_architecture
  }

  container_definitions = jsonencode([{
    name        = "server"
    image       = var.server_image
    essential   = true
    environment = local.common_environment
    secrets     = local.database_secret

    portMappings = [{ containerPort = var.container_port, protocol = "tcp" }]

    logConfiguration = {
      logDriver = "awslogs"
      options = {
        "awslogs-group"         = aws_cloudwatch_log_group.server.name
        "awslogs-region"        = var.region
        "awslogs-stream-prefix" = "server"
      }
    }

    healthCheck = {
      command     = ["CMD-SHELL", "wget -q -O- http://127.0.0.1:${var.container_port}/health || exit 1"]
      interval    = 30
      timeout     = 5
      retries     = 3
      startPeriod = 30
    }
  }])
}

resource "aws_ecs_task_definition" "worker" {
  family                   = "${var.name}-worker"
  requires_compatibilities = ["FARGATE"]
  network_mode             = "awsvpc"
  cpu                      = var.worker_cpu
  memory                   = var.worker_memory
  execution_role_arn       = aws_iam_role.execution.arn
  task_role_arn            = aws_iam_role.worker.arn

  runtime_platform {
    operating_system_family = "LINUX"
    cpu_architecture        = var.cpu_architecture
  }

  container_definitions = jsonencode([{
    name      = "worker"
    image     = var.worker_image
    essential = true
    environment = concat(local.common_environment, [
      { name = "KEYCLOAK_CLIENT_ID", value = var.keycloak_client_id },
      { name = "WORKER_VISIBILITY_TIMEOUT", value = tostring(var.queue_visibility_timeout) },
      { name = "WORKER_POLL_TIMEOUT", value = "20s" },
      { name = "WORKER_CONCURRENCY", value = tostring(var.worker_concurrency) },
    ])
    secrets = concat(local.database_secret, [
      { name = "KEYCLOAK_CLIENT_SECRET", valueFrom = var.keycloak_client_secret_arn },
    ])

    # The worker serves its own probe, which is how the orchestrator knows a consumer is alive.
    portMappings = [{ containerPort = var.container_port, protocol = "tcp" }]

    logConfiguration = {
      logDriver = "awslogs"
      options = {
        "awslogs-group"         = aws_cloudwatch_log_group.worker.name
        "awslogs-region"        = var.region
        "awslogs-stream-prefix" = "worker"
      }
    }

    healthCheck = {
      command     = ["CMD-SHELL", "wget -q -O- http://127.0.0.1:${var.container_port}/health || exit 1"]
      interval    = 30
      timeout     = 5
      retries     = 3
      startPeriod = 30
    }
  }])
}

# --- services ------------------------------------------------------------------------------

resource "aws_ecs_service" "server" {
  name            = "${var.name}-server"
  cluster         = aws_ecs_cluster.this.id
  task_definition = aws_ecs_task_definition.server.arn
  desired_count   = var.server_replicas
  launch_type     = "FARGATE"

  network_configuration {
    subnets = var.private_subnet_ids
    # No public address: the only way in is the load balancer.
    assign_public_ip = false
    security_groups  = [aws_security_group.tasks.id]
  }

  load_balancer {
    target_group_arn = var.target_group_arn
    container_name   = "server"
    container_port   = var.container_port
  }

  # Gives the task time to load the realm metadata before the target group starts counting
  # failed health checks against it.
  health_check_grace_period_seconds = 60

  deployment_circuit_breaker {
    enable   = true
    rollback = true
  }

  # Lets the deploy pipeline move the tag without Terraform trying to put it back.
  lifecycle {
    ignore_changes = [task_definition, desired_count]
  }

  depends_on = [var.alb_listener_arns]
}

resource "aws_ecs_service" "worker" {
  name            = "${var.name}-worker"
  cluster         = aws_ecs_cluster.this.id
  task_definition = aws_ecs_task_definition.worker.arn
  desired_count   = var.worker_replicas
  launch_type     = "FARGATE"

  network_configuration {
    subnets          = var.private_subnet_ids
    assign_public_ip = false
    security_groups  = [aws_security_group.tasks.id]
  }

  deployment_circuit_breaker {
    enable   = true
    rollback = true
  }

  lifecycle {
    ignore_changes = [task_definition, desired_count]
  }
}
