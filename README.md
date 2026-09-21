# pedro-test

Backend Go de carteira e apostas: **Hexagonal (Ports & Adapters)**, dois processos saindo do mesmo
código (servidor HTTP e worker de fila), **Uber `fx`** para injeção de dependência e ciclo de
vida, **Keycloak** como IDP, **SQS** como fila, **pgx** no Postgres, **Swagger** (swag + Swagger
UI) na doc de API, **OpenTelemetry** exportando direto (sem coletor) e **`zap`** no log.

O domínio implementado é o de **carteira e apostas de provedores** (`SPEC.md`): abrir carteira,
receber `BET`/`WIN`/`LOSS`/`REFUND`/`ROLLBACK` por HTTP ou por fila, manter o saldo e o ledger
consistentes sob concorrência, publicar eventos por outbox e conciliar saldo contra ledger.
Dinheiro é `int64` em unidades menores (escala 2), nunca `float`.

| Documento | Para quê |
|---|---|
| [SPEC.md](SPEC.md) | o desafio: o que o serviço precisa fazer |
| [SPEC-claude.md](SPEC-claude.md) | o plano de implementação, a definição de pronto e os cenários e2e em Gherkin |
| [ARCHITECTURE.md](ARCHITECTURE.md) | **por que** cada decisão foi tomada; a lógica de negócio (§2), o modelo de dados (§3) e os fluxos (§4) |
| [CLAUDE.md](CLAUDE.md) | as regras de como escrever código aqui |
| [infra/README.md](infra/README.md) | Terraform (VPC, ALB, ECS, RDS, SQS) |

<!-- indice:inicio -->
## Índice

- [Pré-requisitos](#pré-requisitos)
- [Os componentes](#os-componentes)
- [Subindo com docker-compose](#subindo-com-docker-compose)
  - [Doc da API: ver, pegar token e testar no navegador](#doc-da-api-ver-pegar-token-e-testar-no-navegador)
  - [Exercitando](#exercitando)
  - [Exercitando a fila](#exercitando-a-fila)
- [Lendo a telemetria](#lendo-a-telemetria)
  - [Traces (Tempo)](#traces-tempo)
  - [Métricas (Prometheus)](#métricas-prometheus)
  - [Sem abrir o navegador](#sem-abrir-o-navegador)
  - [Logs](#logs)
- [Comandos](#comandos)
  - [Variáveis de ambiente](#variáveis-de-ambiente)
  - [Fora do Docker](#fora-do-docker)
  - [Testes](#testes)
- [Estrutura](#estrutura)
<!-- indice:fim -->

---

## Pré-requisitos

| Precisa de | Para | Versão |
|---|---|---|
| Docker (com Compose v2) | subir a stack e rodar a suíte e2e | qualquer recente |
| Go | compilar, rodar os testes fora do Docker | **1.26** (declarada em `go.mod` e no `Dockerfile`) |
| `make` | atalhos (`make help` lista) | — |
| `goose` | migrations fora do Docker | `go install github.com/pressly/goose/v3/cmd/goose@v3.28.0` |
| `swag` | só para regenerar a doc da API (`make docs`) | `go install github.com/swaggo/swag/cmd/swag@v1.16.6` |

No Colima, o socket do Docker não fica em `/var/run/docker.sock`: `make test-e2e` já aponta
`DOCKER_HOST` para o do Colima (sobrescreva a variável se o seu for outro).

---

## Os componentes

```
                    ┌──────────────────────────────────────────────┐
   Bearer JWT       │                                              │
   ──────────────▶  │   server            :3000                    │
                    │   Echo + handlers ─▶ services ─▶ repositories│
                    └──────┬──────────────┬────────────┬───────────┘
                           │              │            │
             valida token  │              │ dado       │ publica
                           ▼              ▼            ▼
                    ┌──────────┐   ┌──────────┐   ┌────────┐
                    │ Keycloak │   │ Postgres │   │  SQS   │
                    └──────────┘   └──────────┘   └───┬────┘
                           ▲              ▲           │ consome
            service account│              │           ▼
                    ┌──────┴──────────────────────┴───────────────┐
                    │   worker            :3010 (probe)           │
                    │   jobrunner ─▶ handlers ─▶ services ─▶ repos │
                    │   cronjob   ─▶ outbox (publica eventos)      │
                    │             ─▶ referências pendentes         │
                    └─────────────────────────────────────────────┘

                    ambos exportam OTLP ──▶ Grafana (Tempo · Prometheus)
```

O worker faz **pull** do SQS e o resultado do handler decide o destino da mensagem:

```
handler devolve nil    ─▶ DeleteMessage: a mensagem some da fila
handler devolve erro   ─▶ nada é apagado: volta a ficar visível quando o
                          visibility timeout expira, e é entregue de novo — até a DLQ
```

Não existe terceira saída. Disso decorre que **todo handler é idempotente** e que **falha que
retry não conserta devolve `nil`**.

Os dois processos compartilham **tudo abaixo da entrega**: config, telemetria, pools,
repositórios e services. O que muda é o que cada `main` monta.

| | Server | Worker |
|---|---|---|
| Runtime | Echo, em `app/cmd/server/main.go` | `libs/jobrunner` |
| Entrega | `handlers/<dom>.HTTPHandler` | `handlers/<dom>.JobHandler` |
| Regra | `services/<dom>.Service` | `services/<dom>.CompletionService` |
| Dado | `repositories/<dom>.PostgresRepository` | o mesmo |
| Identidade | valida o JWT de quem chamou | service account (`client_credentials`) |

Cada domínio expõe `ServerRoutes` (rotas) e `PrepareWorker` (mensagem de fila); as tarefas
periódicas são registradas pela própria `main` do worker (`Runner.Register`), que é o único lugar
que decide o que roda em cada processo. No vocabulário daqui, **worker** é o
processo, **job** é uma mensagem do SQS e **cronjob** é um tick periódico (`libs/cronjob`). Detalhes em [ARCHITECTURE §4](ARCHITECTURE.md#7-um-source-code-dois-entrypoints).

---

## Subindo com docker-compose

```bash
make up      # sobe tudo e faz o build das duas imagens
make logs    # acompanha server e worker
```

O compose sobe, nesta ordem: `postgres`, `localstack` (SQS), `keycloak`,
`observability`, o `migrate` (goose, roda e sai) e só então `server` e `worker`.

| Serviço | Endereço |
|---|---|
| Server | <http://localhost:3000> |
| **Doc da API** | <http://localhost:3000/docs> |
| Probe do worker | <http://localhost:3010/health/live> · `/health/ready` |
| Grafana | <http://localhost:3001> |
| Keycloak | <http://localhost:8080> (`admin` / `admin`) |
| LocalStack (SQS) | <http://localhost:4566> |

Usuários do realm importado: `pedro`/`pedro` (com o papel `app-admin`) e `joana`/`joana`
(sem papel).

```bash
make down    # derruba tudo e apaga os volumes
```

### Doc da API: ver, pegar token e testar no navegador

Abra <http://localhost:3000/docs> (redireciona para `/swagger/index.html`). É o **Swagger UI**,
servindo o documento que o `swag` gera a partir das anotações ao lado de cada handler.

Para chamar uma rota protegida direto dali:

1. Clique em **Authorize** (cadeado no topo).
2. O **client_id** já vem preenchido com `pedro-test-api`; preencha usuário `pedro` e senha
   `pedro`.
3. **Authorize** — a página faz o *password grant* contra o Keycloak e guarda o token. Ele
   persiste entre reloads.
4. Abra `GET /me`, **Try it out**, execute — a resposta traz o que o realm disse sobre você.

**Rotas de carteira e de aposta pelo Swagger UI.** O login acima é de uma *pessoa*, e nenhuma
pessoa do realm tem `internal_service` nem `provider` (`pedro` é `app-admin`): com esse token, as rotas
de carteira e de aposta respondem `403`. Para essas, use o segundo esquema do **Authorize**,
**BearerAuth**, e cole `Bearer <token>` de um dos clients de serviço:

```bash
make token-internal   # pedro-test-wallet-service: abre carteira, lê carteira e ledger, reconcilia
make token-provider   # provider-a: envia e lê operações (o providerId do corpo tem que ser provider-a)
```

Cada rota aceita um esquema **ou** o outro. O token vale 15 minutos; peça outro quando expirar.

O documento cru está em <http://localhost:3000/swagger/doc.json>, útil para gerar client ou
importar no Insomnia/Bruno.

> A rota só existe com `DOCS_ENABLED=true` — ligada no compose, **desligada por padrão**. Com a
> flag off os caminhos nem são registrados: a resposta é 404, não 403. O plano para expor a doc
> em produção, se um dia for o caso, é Cloudflare Access na borda — está no
> [TO DO da arquitetura](ARCHITECTURE.md#4-cloudflare-access-na-doc-da-api-se-formos-expor-em-produção).

Se preferir o terminal, `make token` imprime um access token e os `curl` abaixo funcionam igual.

### Exercitando

Dois papéis, dois clients do realm (`client_credentials`): o serviço interno abre e consulta
carteiras (`internal_service`); o provedor envia operações (`provider`). O `providerId` do corpo
tem que ser o do token (`provider_id`), senão é 403.

```bash
KC=http://localhost:8080/realms/pedro-test/protocol/openid-connect/token
token() { curl -s -X POST $KC -d grant_type=client_credentials -d client_id=$1 -d client_secret=$2 | jq -r .access_token; }

INTERNAL=$(token pedro-test-wallet-service wallet-service-secret-local)
PROVIDER=$(token provider-a provider-a-secret-local)
PLAYER_ID=$(uuidgen | tr A-Z a-z)

# abre a carteira (201). O saldo inicial vira uma transação OPENING e uma linha de ledger.
WALLET_ID=$(curl -s localhost:3000/wallets -H "Authorization: Bearer $INTERNAL" \
  -H 'Content-Type: application/json' \
  -d '{"playerId":"'$PLAYER_ID'","initialBalance":{"amount":"1000.00","currency":"BRL"}}' | jq -r .id)

# uma aposta (200). Repetir a mesma chamada devolve o resultado guardado, com idempotentReplay: true.
BET='{"providerId":"provider-a","externalTransactionId":"bet-1","playerId":"'$PLAYER_ID'",
      "walletId":"'$WALLET_ID'","roundId":"round-1","gameId":"fortune-chimp","kind":"BET",
      "money":{"amount":"25.00","currency":"BRL"}}'
curl -s localhost:3000/wagering/transactions -H "Authorization: Bearer $PROVIDER" \
  -H 'Content-Type: application/json' -H 'Idempotency-Key: provider-a:bet-1' -d "$BET"

# um REFUND que chega antes da aposta fica PENDING_REFERENCE (202) e se resolve quando ela chega.
curl -s localhost:3000/wallets/$WALLET_ID -H "Authorization: Bearer $INTERNAL"
curl -s "localhost:3000/wallets/$WALLET_ID/ledger?limit=50" -H "Authorization: Bearer $INTERNAL"
curl -s -X POST localhost:3000/wallets/$WALLET_ID/reconciliation -H "Authorization: Bearer $INTERNAL"
```

| Método | Rota | Quem | O que faz |
|---|---|---|---|
| `POST` | `/wallets` | `internal_service` | abre a carteira (uma por jogador e moeda; 409 se já existe) |
| `GET` | `/wallets/{id}` | `internal_service` | saldo e versão |
| `GET` | `/wallets/{id}/ledger` | `internal_service` | ledger paginado por cursor opaco (`limit` ≤ 200, padrão 50) |
| `POST` | `/wallets/{id}/reconciliation` | `internal_service` | compara o saldo guardado com a soma do ledger, sem alterar nada |
| `POST` | `/wagering/transactions` | `provider` | envia uma operação; exige `Idempotency-Key` |
| `GET` | `/wagering/transactions/{id}` | `provider` | lê uma transação própria |
| `GET` | `/providers/{providerId}/wagering/transactions/{externalId}` | `provider` | lê pelo id do provedor |
| `GET` | `/health`, `/health/live`, `/health/ready` | ninguém | probes; `ready` checa Postgres e SQS e devolve 503 nomeando o que falhou |
| `GET` | `/docs`, `/swagger/*` | ninguém | só existem com `DOCS_ENABLED` |
| `GET` | `/me` | Bearer JWT | quem o realm diz que você é |

Falha responde sempre a mesma forma (`{"message": "...", "failureCode": "..."}`): **400** entrada
inválida, **401** sem token, **403** papel ou provedor errado, **404** não existe (ou é de outro
provedor), **409** conflito de idempotência, **422** regra de negócio (`INSUFFICIENT_FUNDS`,
`CURRENCY_MISMATCH`, `REFERENCE_ALREADY_REVERSED`...), **202** referência pendente.

### Exercitando a fila

As filas são FIFO e criadas por `docker/localstack/init-queues.sh` (a aplicação nunca cria infra):
`wager-transactions.fifo` (entrada, consumida pelo worker), `wager-transactions-dlq.fifo` e
`wager-events.fifo` (saída: o publisher do outbox envia os eventos `WagerTransactionProcessed`,
`WagerTransactionRejected`, `WalletBalanceChanged` e `WagerTransactionPendingReference`, com
`MessageGroupId` = agregado e `MessageDeduplicationId` = id do evento). Publique direto no SQS do LocalStack e veja o worker consumir. O
`MessageGroupId` é a carteira (as operações de uma carteira são consumidas uma de cada vez, em
ordem) e o `MessageDeduplicationId` é a chave de idempotência:

```bash
aws --endpoint-url=http://localhost:4566 sqs send-message \
  --queue-url http://localhost:4566/000000000000/wager-transactions.fifo \
  --message-group-id "$WALLET_ID" \
  --message-deduplication-id "provider-a:transaction-123" \
  --message-body '{
    "messageId": "msg-123",
    "type": "WagerTransactionRequested",
    "occurredAt": "2026-09-08T12:00:00.000Z",
    "data": {
      "providerId": "provider-a",
      "externalTransactionId": "transaction-123",
      "idempotencyKey": "provider-a:transaction-123",
      "playerId": "'"$PLAYER_ID"'",
      "walletId": "'"$WALLET_ID"'",
      "roundId": "round-987",
      "gameId": "fortune-chimp",
      "kind": "BET",
      "money": { "amount": "25.00", "currency": "BRL" }
    }
  }'

docker compose logs -f worker
```

O que nenhuma retentativa conserta (corpo malformado, tipo desconhecido, conflito de idempotência)
vai para `wager-transactions-dlq.fifo` com o motivo no atributo `failureReason`, e sai da fila
principal.

---

## Lendo a telemetria

A aplicação fala **OTLP direto** com o container `observability` (`grafana/otel-lgtm`), que
traz ingestão, armazenamento e Grafana num contêiner só. **Não há coletor no meio.**

Abra <http://localhost:3001> (sem login) e vá em **Explore**.

### Traces (Tempo)

Selecione o datasource **Tempo**, aba **TraceQL**, e cole:

```traceql
{ resource.service.name = "pedro-test-server" }
```

Abrindo o trace de uma requisição, as camadas aparecem uma dentro da outra. Quando a operação
passa pela fila, o trace continua no worker — o `traceparent` viaja nas *message attributes*
do SQS, então quem publicou e o worker compartilham o `trace_id` e o tempo de fila fica visível
entre os dois:

```
GET /me                                pedro-test-server   (span do servidor)
└─ GET /me                               app.layer=handler
   └─ Keycloak.Verify                    app.layer=gateway

POST /wagering/transactions            pedro-test-server
└─ POST /wagering/transactions           app.layer=handler
   └─ wagering.Service.Submit            app.layer=service
      ├─ wagering.Repository.FindByKey   app.layer=repository
      └─ persistence.UnitOfWork.Atomic   app.layer=repository

wagering.JobHandler.Handle             pedro-test-worker   app.layer=handler   (mesmo trace_id da mensagem)
└─ wagering.Service.Receive              app.layer=service
```

Consultas que valem guardar:

```traceql
{ span.app.layer = "repository" }                      # onde o tempo foi gasto em I/O
{ span.app.layer = "repository" && duration > 100ms }  # query lenta
{ resource.service.name = "pedro-test-worker" }        # só o worker
{ status = error }                                     # tudo que falhou
{ span.app.operation = "queue.Publish" }               # uma operação específica
```

### Métricas (Prometheus)

Datasource **Prometheus**, aba **Code**:

```promql
# throughput por camada e operação
sum by (app_layer, app_operation) (rate(app_operation_duration_milliseconds_count[5m]))

# p95 por operação
histogram_quantile(0.95,
  sum by (le, app_operation) (rate(app_operation_duration_milliseconds_bucket[5m])))

# erros por camada — é isso que diz em qual fronteira o problema nasceu
sum by (app_layer, app_operation) (rate(app_operation_failures_total[5m]))

# comparando as duas aplicações
sum by (service_name) (rate(app_operation_duration_milliseconds_count[5m]))
```

Rótulos disponíveis: `app_layer` (`handler`, `service`, `repository`, `gateway`),
`app_operation`, `app_status`, `service_name`, `service_namespace`, `service_version`.

> A métrica aparece no próximo ciclo de exportação — `OTEL_METRIC_EXPORT_INTERVAL`, 15s no
> compose e 60s por padrão. Se a consulta vier vazia logo após subir, é só isso.

Além das métricas por camada, o domínio emite as suas (`wager_transactions_total{kind,status,failure_code}`,
`wager_idempotent_replays_total`, `inbox_duplicates_total`, `outbox_publish_attempts_total`,
`sqs_dead_letters_total`, `reconciliation_divergences_total`...). A lista completa, com tags, está
em [ARCHITECTURE §2.9](ARCHITECTURE.md#29-observabilidade-do-domínio). Todo log de operação carrega
`correlationId`, `walletId`, `providerId` e `transactionId`, e nunca valores monetários nem tokens.

### Sem abrir o navegador

O Grafana expõe os datasources por proxy, então dá para ler tudo com `curl`:

```bash
# traces de um serviço
curl -s -G http://localhost:3001/api/datasources/proxy/uid/tempo/api/search \
  --data-urlencode 'q={ resource.service.name = "pedro-test-server" }' | jq '.traces[].traceID'

# um trace inteiro
curl -s http://localhost:3001/api/datasources/proxy/uid/tempo/api/traces/<TRACE_ID> | jq

# quais métricas existem
curl -s -G http://localhost:3001/api/datasources/proxy/uid/prometheus/api/v1/label/__name__/values \
  | jq '.data[] | select(startswith("app_"))'

# consulta instantânea
curl -s -G http://localhost:3001/api/datasources/proxy/uid/prometheus/api/v1/query \
  --data-urlencode 'query=sum by (app_layer) (app_operation_failures_total)' | jq '.data.result'
```

### Logs

O log é estruturado e vai para a saída padrão (`make logs`). **Toda linha emitida dentro de um
span carrega `trace_id` e `span_id`**, então um erro visto no log leva direto ao trace:

```bash
docker compose logs server | grep '"level":"error"'
# copie o trace_id e cole na busca do Tempo
```

> Trocar Grafana por New Relic, Datadog ou X-Ray é mudar `OTEL_EXPORTER_OTLP_ENDPOINT` —
> nenhuma linha de Go muda. O porquê disso está em
> [ARCHITECTURE §8](ARCHITECTURE.md#por-que-opentelemetry-e-não-o-sdk-de-um-fornecedor).

---

## Comandos

```bash
make help    # lista tudo
```

**Desenvolvimento**

| Comando | O que faz |
|---|---|
| `make verify` | **a definição de pronto inteira**: `gofmt`, `go vet`, `lint`, `test`, `coverage` e `test-e2e` |
| `make test` | unitários (services + lógica em `entities`/`structs`), com race detector |
| `make coverage` | cobertura de `services/`, falha abaixo de 80% |
| `make lint` | `golangci-lint` — é ele que cobra as regras de camada |
| `make test-e2e` | fluxos de ponta a ponta com testcontainers (Postgres, LocalStack, Keycloak) |
| `make docs` | regenera o documento Swagger a partir das anotações |
| `make build` | compila os dois entrypoints em `bin/` |
| `make tidy` | arruma o `go.mod` |

**Stack local**

| Comando | O que faz |
|---|---|
| `make up` | sobe a stack completa |
| `make down` | derruba e apaga os volumes |
| `make logs` | segue o log de `server` e `worker` |
| `make token` | imprime um access token (`pedro`/`pedro`) |

**Migrações** (goose)

| Comando | O que faz |
|---|---|
| `make migrate-create NAME=...` | cria o par up/down |
| `make migrate-up` | aplica as pendentes |
| `make migrate-down` | desfaz a última |
| `make migrate-status` | mostra o que está aplicado |

### Variáveis de ambiente

Todas estão comentadas em [`.env.example`](.env.example). As que importam para este domínio:

| Variável | Para quê |
|---|---|
| `DATABASE_URL` | Postgres (o schema vem do goose; a aplicação não migra) |
| `KEYCLOAK_ISSUER` · `KEYCLOAK_INTERNAL_URL` · `KEYCLOAK_AUDIENCE` | endereço externo (claim `iss`) e interno do realm |
| `SQS_QUEUE_URL` · `SQS_DLQ_URL` · `SQS_EVENTS_QUEUE_URL` | entrada, dead letter e eventos de saída |
| `WORKER_POLL_TIMEOUT` · `WORKER_VISIBILITY_TIMEOUT` · `WORKER_CONCURRENCY` | consumo da fila |
| `OUTBOX_POLL_INTERVAL` · `OUTBOX_BATCH_SIZE` · `OUTBOX_LEASE` · `OUTBOX_BACKOFF_*` | publisher do outbox (nunca desiste de um evento) |
| `REFERENCE_TTL` · `REFERENCE_MAX_ATTEMPTS` · `REFERENCE_BACKOFF_*` · `REFERENCE_POLL_INTERVAL` | espera de REFUND/ROLLBACK cuja referência ainda não chegou |
| `OTEL_EXPORTER_OTLP_ENDPOINT` | vazio desliga o export (é o que os testes fazem) |

### Fora do Docker

Com Postgres, LocalStack e Keycloak no ar:

```bash
cp .env.example .env
go install github.com/pressly/goose/v3/cmd/goose@v3.28.0
docker compose up -d postgres localstack keycloak   # ou os seus, nas mesmas portas do .env.example
make migrate-up      # make migrate-down desfaz a última; make migrate-status mostra o estado
make run-server      # noutro terminal: make run-worker
```

### Testes

```bash
go test ./...        # unitários, sem Docker (a suíte e2e fica atrás da build tag e2e)
go test -race ./...  # o mesmo com o detector de corrida (é o que `make test` roda)
go vet ./...

make coverage        # piso de 80% em services/ (hoje ~89%)
make test-e2e        # precisa de Docker: Postgres, LocalStack e Keycloak em containers
make verify          # tudo acima mais gofmt, go vet e lint
```

O `make test-e2e` é este comando, com o `DOCKER_HOST` do Colima quando ele não está definido:

```bash
TESTCONTAINERS_RYUK_DISABLED=true go test -race -tags e2e -count=1 -timeout 30m ./app/test/...
go test -race -tags e2e -count=1 -run 'TestFiftyParallel' ./app/test/...   # um cenário só
```

**Preparar as dependências dos testes:** nada além do Docker. Os containers (Postgres, LocalStack
com as filas criadas por `docker/localstack/init-queues.sh`, Keycloak com o realm importado) são
criados pela própria suíte, as migrations são aplicadas por ela e tudo é destruído no fim. A
primeira execução baixa as imagens; depois disso a suíte leva cerca de dois minutos.

**Integração, múltiplas instâncias e simulação de falha** estão todas nessa mesma suíte, por
arquivo (`app/test/e2e/`): `concurrency_test.go` (50 apostas iguais, 80+80 sobre 100, três
instâncias, HTTP × SQS), `recovery_test.go` (kill entre commit e ack, retomada por outra instância,
restart, `SIGTERM` num processo real), `outbox_test.go` (publishers concorrentes, broker fora do ar,
queda entre publicar e confirmar) e `lifecycle_and_health_test.go` (boot, shutdown, readiness com
Postgres ou SQS fora). As falhas são injetadas nas portas por `app/test/e2e/core/faults.go`.

A suíte e2e (`app/test/e2e`) tem uma Feature por arquivo, em Gherkin nos comentários; os cenários
estão listados em [SPEC-claude.md](SPEC-claude.md). Ela sobe o servidor e o worker no mesmo
processo, mais instâncias independentes quando o cenário exige (corridas entre instâncias,
publishers concorrentes) e o binário do worker como processo de verdade no cenário de SIGTERM.

---

## Estrutura

```
app/
├── cmd/{server,worker}/    o que cada processo monta
├── src/
│   ├── interfaces/<dom>/   os contratos. Folha: as três camadas apontam para cá.
│   ├── handlers/<dom>/     entrega: rotas HTTP (com as anotações da doc) e fila
│   ├── services/<dom>/     regra de negócio
│   ├── repositories/<dom>/ adapters de I/O (pgx, SQS)
│   ├── entities/ structs/  domínio e o que atravessa camadas
│   └── libs/               config, observabilidade, middleware, auth, db (UnitOfWork), runtime da fila (jobrunner) e dos cronjobs
└── test/e2e/               testcontainers: fluxos de ponta a ponta
docker/                     Dockerfile dos apps e do goose, realm, init do LocalStack
infra/                      Terraform (sandbox e prod)
migrations/                 SQL do goose
```

Domínio novo = uma pasta em cada camada que ele toca, mais a linha no `module.go` da camada e
no `main` do processo. As convenções estão em [CLAUDE.md §3](CLAUDE.md#3-camadas).
