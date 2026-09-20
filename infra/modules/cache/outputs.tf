output "primary_endpoint" {
  description = "Hostname of the primary node."
  value       = aws_elasticache_replication_group.this.primary_endpoint_address
}

output "url" {
  description = "Connection URL in the form the application expects."
  value       = "redis://${aws_elasticache_replication_group.this.primary_endpoint_address}:6379/0"
}

output "security_group_id" {
  description = "Security group of the cache."
  value       = aws_security_group.cache.id
}
