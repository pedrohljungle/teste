output "cluster_name" {
  description = "Name of the cluster."
  value       = aws_ecs_cluster.this.name
}

output "task_security_group_id" {
  description = "Security group of the tasks. The database allows this group and nothing else."
  value       = aws_security_group.tasks.id
}

output "server_service_name" {
  description = "Name of the server service."
  value       = aws_ecs_service.server.name
}

output "worker_service_name" {
  description = "Name of the worker service."
  value       = aws_ecs_service.worker.name
}
