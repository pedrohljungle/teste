# The workspace is the environment. Nothing else selects it, so there is no way to apply the
# prod sizing to sandbox by passing the wrong variable file.
locals {
  environment = terraform.workspace

  defaults = {
    sandbox = {
      # A single NAT gateway is a single point of failure and the cheapest one: in sandbox the
      # blast radius of losing egress for an hour is a developer waiting.
      single_nat_gateway = true

      database_instance_class      = "db.t4g.micro"
      database_storage_gb          = 20
      database_multi_az            = false
      database_backup_days         = 1
      database_deletion_protection = false

      server_cpu      = 256
      server_memory   = 512
      server_replicas = 1

      worker_cpu      = 256
      worker_memory   = 512
      worker_replicas = 1

      log_retention_days = 7
    }

    prod = {
      # One NAT per AZ: with a single one, losing its AZ takes egress away from both.
      single_nat_gateway = false

      database_instance_class      = "db.t4g.small"
      database_storage_gb          = 100
      database_multi_az            = true
      database_backup_days         = 14
      database_deletion_protection = true

      server_cpu      = 512
      server_memory   = 1024
      server_replicas = 2

      worker_cpu      = 512
      worker_memory   = 1024
      worker_replicas = 2

      log_retention_days = 30
    }
  }

  # Selecting a workspace is mandatory. Indexing the map is what makes the default workspace
  # unusable: there is no "default" environment, and falling back to one would mean applying
  # the wrong sizing — or applying to the wrong account — because somebody forgot a command.
  config = local.defaults[local.environment]

  name = "${var.project}-${local.environment}"
}
