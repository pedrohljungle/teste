# infra

Terraform da infraestrutura do `pedro-test`. O **porquê** de cada decisão está em
[../ARCHITECTURE.md](../ARCHITECTURE.md#19-infraestrutura-terraform-em-dois-workspaces); aqui está
o operacional.

> Nada disto foi aplicado. É o desenho pronto para `plan`, com as contas e os endereços de
> exemplo em `envs/*.tfvars` para trocar.

## Estado e ambientes

O estado fica no S3, um arquivo por workspace (`workspaces/<sandbox|prod>/pedro-test.tfstate`).
**O workspace é o ambiente** — não há variável que escolha isso, e o workspace `default` não é
um ambiente: um `plan` nele falha de propósito.

O bucket não é criado aqui (um backend não pode depender do estado que ele guarda). Uma vez,
fora do Terraform:

```bash
aws s3api create-bucket --bucket estrategia-pedro-test-tfstate --region us-east-1
aws s3api put-bucket-versioning --bucket estrategia-pedro-test-tfstate \
  --versioning-configuration Status=Enabled
aws s3api put-bucket-encryption --bucket estrategia-pedro-test-tfstate \
  --server-side-encryption-configuration '{"Rules":[{"ApplyServerSideEncryptionByDefault":{"SSEAlgorithm":"AES256"}}]}'
aws s3api put-public-access-block --bucket estrategia-pedro-test-tfstate \
  --public-access-block-configuration BlockPublicAcls=true,IgnorePublicAcls=true,BlockPublicPolicy=true,RestrictPublicBuckets=true
```

O lock usa o arquivo de lock nativo do S3 (`use_lockfile`), então não há tabela DynamoDB.

## Rodando

```bash
terraform init

terraform workspace new sandbox      # só na primeira vez
terraform workspace select sandbox
terraform plan  -var-file=envs/sandbox.tfvars
terraform apply -var-file=envs/sandbox.tfvars

terraform workspace select prod
terraform plan  -var-file=envs/prod.tfvars
```

**O sizing não está no `.tfvars`.** Ele vive em `locals.tf`, indexado pelo workspace: instance
class, réplicas, multi-AZ, retenção e NAT por AZ saem de lá. O que está no `.tfvars` é o que
de fato difere por ambiente e não é tamanho — imagem, issuer do IDP, certificado, CIDR.

## Segredos

Nenhuma senha passa pelo Terraform nem pelo estado:

- **Banco**: o RDS cria e rotaciona a senha do master no Secrets Manager
  (`manage_master_user_password`). A task recebe `DATABASE_PASSWORD` como *secret* do ECS, e o
  `DATABASE_URL` vai sem senha.
- **Keycloak do worker**: o segredo do client é criado fora e referenciado por ARN
  (`keycloak_client_secret_arn`).

## O mapa de conectividade

Está todo em [`security.tf`](security.tf), na raiz, e não espalhado pelos módulos. Toda regra
nomeia um **security group**, nunca um CIDR.

```
internet ──80/443──> alb ──3000──> tasks ──5432──> rds
                                        ──6379──> redis
```

## Comandos úteis

```bash
terraform fmt -recursive
terraform validate            # exige um workspace selecionado
terraform plan  -var-file=envs/sandbox.tfvars -out=tfplan
terraform show -json tfplan | jq '.resource_changes[].address'
```
