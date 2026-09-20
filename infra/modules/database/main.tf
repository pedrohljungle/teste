# Postgres on Graviton (t4g), in the private subnets, reachable only from the tasks.

resource "aws_db_subnet_group" "this" {
  name       = "${var.name}-db"
  subnet_ids = var.private_subnet_ids

  tags = { Name = "${var.name}-db" }
}

resource "aws_security_group" "database" {
  name        = "${var.name}-db"
  description = "Postgres, private only"
  vpc_id      = var.vpc_id

  tags = { Name = "${var.name}-db" }
}

resource "aws_db_parameter_group" "this" {
  name        = "${var.name}-pg17"
  family      = "postgres17"
  description = "${var.name} postgres parameters"

  # Log any statement above a second. The default is off, which means the first time a query
  # gets slow nobody has the evidence.
  parameter {
    name  = "log_min_duration_statement"
    value = "1000"
  }

  lifecycle {
    create_before_destroy = true
  }
}

resource "aws_db_instance" "this" {
  identifier     = "${var.name}-pg"
  engine         = "postgres"
  engine_version = var.engine_version
  instance_class = var.instance_class

  allocated_storage     = var.storage_gb
  max_allocated_storage = var.storage_gb * 4 # storage autoscaling, so a full disk is not an outage
  storage_type          = "gp3"
  storage_encrypted     = true

  db_name  = var.database_name
  username = var.master_username
  # RDS creates and rotates the password in Secrets Manager. Nothing about it passes through
  # Terraform state, which is what keeps the state file from being a credential store.
  manage_master_user_password = true

  db_subnet_group_name   = aws_db_subnet_group.this.name
  vpc_security_group_ids = [aws_security_group.database.id]
  parameter_group_name   = aws_db_parameter_group.this.name
  publicly_accessible    = false

  multi_az                = var.multi_az
  backup_retention_period = var.backup_days
  backup_window           = "06:00-07:00" # 03:00-04:00 in São Paulo
  maintenance_window      = "sun:07:00-sun:08:00"

  auto_minor_version_upgrade = true
  deletion_protection        = var.deletion_protection
  skip_final_snapshot        = !var.deletion_protection
  final_snapshot_identifier  = var.deletion_protection ? "${var.name}-pg-final" : null

  performance_insights_enabled    = true
  enabled_cloudwatch_logs_exports = ["postgresql"]

  tags = { Name = "${var.name}-pg" }
}
