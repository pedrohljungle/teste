# terraform workspace select sandbox && terraform apply -var-file=envs/sandbox.tfvars
#
# Sizing is NOT here: it comes from locals.tf, keyed by workspace, so selecting the workspace
# is the only thing that decides whether this is the sandbox or the production shape. What
# lives here is what genuinely differs per environment and is not a size.

vpc_cidr = "10.40.0.0/16"

server_image = "000000000000.dkr.ecr.us-east-1.amazonaws.com/pedro-test-server:latest"
worker_image = "000000000000.dkr.ecr.us-east-1.amazonaws.com/pedro-test-worker:latest"

keycloak_issuer   = "https://id.sandbox.example.com/realms/pedro-test"
keycloak_audience = "pedro-test-api"

keycloak_client_secret_arn = "arn:aws:secretsmanager:us-east-1:000000000000:secret:pedro-test/sandbox/keycloak-worker"

# No certificate in sandbox: plain HTTP on the load balancer.
certificate_arn = ""

otlp_endpoint = "otel-collector.sandbox.internal:4317"
