module "network" {
  source = "./modules/network"

  name               = local.name
  vpc_cidr           = var.vpc_cidr
  single_nat_gateway = local.config.single_nat_gateway
}

module "queue" {
  source = "./modules/queue"

  name                       = local.name
  visibility_timeout_seconds = 60
  max_receive_count          = 5
}

module "alb" {
  source = "./modules/alb"

  name                = local.name
  vpc_id              = module.network.vpc_id
  public_subnet_ids   = module.network.public_subnet_ids
  target_port         = var.container_port
  certificate_arn     = var.certificate_arn
  deletion_protection = local.environment == "prod"
}

module "database" {
  source = "./modules/database"

  name                = local.name
  vpc_id              = module.network.vpc_id
  private_subnet_ids  = module.network.private_subnet_ids
  instance_class      = local.config.database_instance_class
  storage_gb          = local.config.database_storage_gb
  multi_az            = local.config.database_multi_az
  backup_days         = local.config.database_backup_days
  deletion_protection = local.config.database_deletion_protection
}

module "cache" {
  source = "./modules/cache"

  name               = local.name
  vpc_id             = module.network.vpc_id
  private_subnet_ids = module.network.private_subnet_ids
  node_type          = local.config.cache_node_type
  node_count         = local.config.cache_node_count
  apply_immediately  = local.environment != "prod"
}

module "ecs" {
  source = "./modules/ecs"

  name        = local.name
  environment = local.environment
  region      = var.region

  vpc_id                = module.network.vpc_id
  private_subnet_ids    = module.network.private_subnet_ids
  alb_security_group_id = module.alb.security_group_id
  target_group_arn      = module.alb.target_group_arn
  alb_listener_arns     = module.alb.listener_arns
  container_port        = var.container_port

  server_image    = var.server_image
  worker_image    = var.worker_image
  server_cpu      = local.config.server_cpu
  server_memory   = local.config.server_memory
  server_replicas = local.config.server_replicas
  worker_cpu      = local.config.worker_cpu
  worker_memory   = local.config.worker_memory
  worker_replicas = local.config.worker_replicas

  # The password is not here: the URL carries user, host and database, and the ECS agent
  # resolves DATABASE_PASSWORD from Secrets Manager at start.
  database_url        = "postgres://${module.database.master_username}@${module.database.address}:5432/${module.database.database_name}?sslmode=require"
  database_secret_arn = module.database.master_secret_arn

  redis_url = module.cache.url

  queue_url                = module.queue.queue_url
  queue_arn                = module.queue.queue_arn
  queue_visibility_timeout = module.queue.visibility_timeout_seconds

  events_queue_url = module.queue.events_queue_url
  events_queue_arn = module.queue.events_queue_arn

  keycloak_issuer            = var.keycloak_issuer
  keycloak_audience          = var.keycloak_audience
  keycloak_client_id         = var.keycloak_client_id
  keycloak_client_secret_arn = var.keycloak_client_secret_arn

  otlp_endpoint      = var.otlp_endpoint
  trace_sample_ratio = local.environment == "prod" ? 0.1 : 1.0
  log_retention_days = local.config.log_retention_days
}
