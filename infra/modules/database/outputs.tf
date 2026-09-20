output "endpoint" {
  description = "host:port of the instance."
  value       = aws_db_instance.this.endpoint
}

output "address" {
  description = "Hostname of the instance."
  value       = aws_db_instance.this.address
}

output "database_name" {
  description = "Database created on the instance."
  value       = aws_db_instance.this.db_name
}

output "master_username" {
  description = "Master user."
  value       = aws_db_instance.this.username
}

output "master_secret_arn" {
  description = "Secrets Manager secret RDS manages the master password in. The tasks read the password from here, never from Terraform state."
  value       = aws_db_instance.this.master_user_secret[0].secret_arn
}

output "security_group_id" {
  description = "Security group of the instance."
  value       = aws_security_group.database.id
}
