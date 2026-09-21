output "vpc_id" {
  description = "Id of the VPC."
  value       = aws_vpc.this.id
}

output "public_subnet_ids" {
  description = "Subnets with a route to the internet gateway. Only the load balancer belongs here."
  value       = aws_subnet.public[*].id
}

output "private_subnet_ids" {
  description = "Subnets that reach the internet only through NAT. Tasks and database belong here."
  value       = aws_subnet.private[*].id
}

output "availability_zones" {
  description = "AZs the subnets were spread across."
  value       = local.azs
}
