# terraform workspace select prod && terraform apply -var-file=envs/prod.tfvars

vpc_cidr = "10.50.0.0/16"

server_image = "000000000000.dkr.ecr.us-east-1.amazonaws.com/pedro-test-server:latest"
worker_image = "000000000000.dkr.ecr.us-east-1.amazonaws.com/pedro-test-worker:latest"

keycloak_issuer   = "https://id.example.com/realms/pedro-test"
keycloak_audience = "pedro-test-api"

keycloak_client_secret_arn = "arn:aws:secretsmanager:us-east-1:000000000000:secret:pedro-test/prod/keycloak-worker"

certificate_arn = "arn:aws:acm:us-east-1:000000000000:certificate/REPLACE-ME"

otlp_endpoint = "otel-collector.internal:4317"
