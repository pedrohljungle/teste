# Who can talk to whom.
#
# The rules live here, and not inside each module, for two reasons. The practical one is that
# they would form a cycle: the database has to allow the tasks, and the tasks have to know the
# database address. The better one is that this file is the connectivity map of the whole
# stack — one screen that answers "what can reach the database?" without opening five modules.
#
# Every rule names a SECURITY GROUP, never a CIDR. Widening access later means naming another
# group, which is an explicit act, instead of a /16 nobody re-reads.
#
#   internet ──80/443──> alb ──3000──> tasks ──5432──> database
#                                            ──6379──> cache
#
# Nothing points back: neither the database nor the cache can open a connection to anything.

resource "aws_vpc_security_group_egress_rule" "alb_to_tasks" {
  security_group_id            = module.alb.security_group_id
  description                  = "Load balancer to the server tasks"
  referenced_security_group_id = module.ecs.task_security_group_id
  from_port                    = var.container_port
  to_port                      = var.container_port
  ip_protocol                  = "tcp"
}

resource "aws_vpc_security_group_ingress_rule" "database_from_tasks" {
  security_group_id            = module.database.security_group_id
  description                  = "Postgres from the application tasks"
  referenced_security_group_id = module.ecs.task_security_group_id
  from_port                    = 5432
  to_port                      = 5432
  ip_protocol                  = "tcp"
}

resource "aws_vpc_security_group_ingress_rule" "cache_from_tasks" {
  security_group_id            = module.cache.security_group_id
  description                  = "Redis from the application tasks"
  referenced_security_group_id = module.ecs.task_security_group_id
  from_port                    = 6379
  to_port                      = 6379
  ip_protocol                  = "tcp"
}
