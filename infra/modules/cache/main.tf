# Redis in the private subnets, reachable only from the tasks. It backs the listing cache; the
# job queue is SQS.

resource "aws_elasticache_subnet_group" "this" {
  name       = "${var.name}-cache"
  subnet_ids = var.private_subnet_ids

  tags = { Name = "${var.name}-cache" }
}

resource "aws_security_group" "cache" {
  name        = "${var.name}-cache"
  description = "Redis, private only"
  vpc_id      = var.vpc_id

  tags = { Name = "${var.name}-cache" }
}

resource "aws_elasticache_replication_group" "this" {
  replication_group_id = "${var.name}-redis"
  description          = "${var.name} listing cache"

  engine         = "redis"
  engine_version = var.engine_version
  node_type      = var.node_type
  port           = 6379

  num_cache_clusters         = var.node_count
  automatic_failover_enabled = var.node_count > 1
  multi_az_enabled           = var.node_count > 1

  subnet_group_name  = aws_elasticache_subnet_group.this.name
  security_group_ids = [aws_security_group.cache.id]

  at_rest_encryption_enabled = true
  # In-transit encryption is off because the client connects with redis:// and turning it on
  # requires rediss:// plus an auth token. The data here is a cached listing, the traffic never
  # leaves the private subnets, and the cost of getting the URL scheme wrong is an outage on a
  # path that should degrade silently. Turn both on together, not one of them.
  transit_encryption_enabled = false

  # A cache that loses its data comes back cold and the listings go to Postgres for a while,
  # which is exactly what the service is written to survive.
  snapshot_retention_limit = 0

  maintenance_window         = "sun:08:00-sun:09:00"
  apply_immediately          = var.apply_immediately
  auto_minor_version_upgrade = true

  tags = { Name = "${var.name}-redis" }
}
