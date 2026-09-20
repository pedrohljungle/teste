output "environment" {
  description = "Workspace this state belongs to."
  value       = local.environment
}

output "alb_dns_name" {
  description = "Public address of the application."
  value       = module.alb.dns_name
}

output "database_endpoint" {
  description = "host:port of the database. Reachable only from the tasks."
  value       = module.database.endpoint
}

output "database_secret_arn" {
  description = "Secret holding the database password."
  value       = module.database.master_secret_arn
}

output "cache_url" {
  description = "Connection URL of the cache. Reachable only from the tasks."
  value       = module.cache.url
}

output "queue_url" {
  description = "URL of the job queue."
  value       = module.queue.queue_url
}

output "ecs_cluster_name" {
  description = "Cluster running both services."
  value       = module.ecs.cluster_name
}

output "private_subnet_ids" {
  description = "Subnets the tasks, the database and the cache run in."
  value       = module.network.private_subnet_ids
}
