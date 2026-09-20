output "dns_name" {
  description = "Public address of the load balancer."
  value       = aws_lb.this.dns_name
}

output "target_group_arn" {
  description = "Target group the server service registers into."
  value       = aws_lb_target_group.server.arn
}

output "security_group_id" {
  description = "Security group of the load balancer."
  value       = aws_security_group.alb.id
}

output "listener_arns" {
  description = "Listeners, so the service can wait for them before registering targets."
  value       = compact([aws_lb_listener.http.arn, try(aws_lb_listener.https[0].arn, "")])
}
