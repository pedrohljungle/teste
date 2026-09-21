# pedro-test

Esqueleto de backend Go: **Hexagonal (Ports & Adapters)**, dois processos saindo do mesmo
código (servidor HTTP e worker de fila), **Uber `fx`** para injeção de dependência e ciclo de
vida, **Keycloak** como IDP, **SQS** como fila, **pgx** no Postgres, **Swagger** (swag + Swagger
UI) na doc de API, **OpenTelemetry** exportando direto (sem coletor) e **`zap`** no log.

> **Não há domínio aqui.** `interfaces/`, `services/` e as pastas de domínio das outras camadas
> estão vazias de propósito: o que existe é a estrutura, a infraestrutura e as duas rotas que
> qualquer serviço tem (`/health` e `/me`). O primeiro domínio é de quem for usar isto.

| Documento | Para quê |
|---|---|
| [ARCHITECTURE.md](ARCHITECTURE.md) | **por que** cada decisão foi tomada, e o que foi descartado |
| [CLAUDE.md](CLAUDE.md) | as regras de como escrever código aqui |
| [infra/README.md](infra/README.md) | Terraform (VPC, ALB, ECS, RDS, Redis, SQS) |

---

## Os componentes

```
                    ┌──────────────────────────────────────────────┐
   Bearer JWT       │                                              │
   ──────────────▶  │   server            :3000                    │
                    │   Echo + handlers ─▶ services ─▶ repositories│
                    └──────┬──────────┬───────────┬────────────┬───┘
                           │          │           │            │
             valida token  │          │ cache     │ dado       │ publica
                           ▼          ▼           ▼            ▼
                    ┌──────────┐ ┌────────┐ ┌──────────┐  ┌────────┐
                    │ Keycloak │ │ Redis  │ │ Postgres │  │  SQS   │
                    └──────────┘ └────────┘ └──────────┘  └───┬────┘
                           ▲                      ▲           │ consome
            service account│                      │           ▼
                    ┌──────┴──────────────────────┴───────────────┐
                    │   worker            :3010 (probe)           │
                    │   jobrunner ─▶ handlers ─▶ services ─▶ repos │
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

Cada domínio expõe `ServerRoutes` e `PrepareWorker`; as `main` chamam o que cada processo
precisa. Detalhes em [ARCHITECTURE §4](ARCHITECTURE.md#7-um-source-code-dois-entrypoints).

---

## Subindo com docker-compose

```bash
make up      # sobe tudo e faz o build das duas imagens
make logs    # acompanha server e worker
```

O compose sobe, nesta ordem: `postgres`, `redis`, `localstack` (SQS), `keycloak`,
`observability`, o `migrate` (goose, roda e sai) e só então `server` e `worker`.

| Serviço | Endereço |
|---|---|
| Server | <http://localhost:3000> |
| **Doc da API** | <http://localhost:3000/docs> |
| Probe do worker | <http://localhost:3010/health> |
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

O documento cru está em <http://localhost:3000/swagger/doc.json>, útil para gerar client ou
importar no Insomnia/Bruno.

> A rota só existe com `DOCS_ENABLED=true` — ligada no compose, **desligada por padrão**. Com a
> flag off os caminhos nem são registrados: a resposta é 404, não 403. O plano para expor a doc
> em produção, se um dia for o caso, é Cloudflare Access na borda — está no
> [TO DO da arquitetura](ARCHITECTURE.md#4-cloudflare-access-na-doc-da-api-se-formos-expor-em-produção).

Se preferir o terminal, `make token` imprime um access token e os `curl` abaixo funcionam igual.

### Exercitando

```bash
TOKEN=$(make -s token)

curl -s localhost:3000/health
curl -s localhost:3000/me -H "Authorization: Bearer $TOKEN"
curl -s -o /dev/null -w '%{http_code}\n' localhost:3000/me     # 401 sem token
```

| Método | Rota | Exige |
|---|---|---|
| `GET` | `/health` | nada (o probe não tem token) |
| `GET` | `/docs`, `/swagger/*` | nada — e só existem com `DOCS_ENABLED` |
| `GET` | `/me` | Bearer JWT |

Falha responde sempre a mesma forma (`{"message": "..."}`): **400** entrada inválida, **401**
sem token, **403** sem papel.

### Exercitando a fila

A fila de operações é FIFO. Publique direto no SQS do LocalStack e veja o worker consumir. O
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

Abrindo o trace de uma requisição, as camadas aparecem uma dentro da outra. Com um domínio que
publique na fila, o trace continua no worker — o `traceparent` viaja nas *message attributes*
do SQS, então servidor e worker compartilham o `trace_id` e o tempo de fila fica visível entre
os dois:

```
GET /me                                pedro-test-server   (span do servidor)
└─ GET /me                               app.layer=handler
   └─ Keycloak.Verify                    app.layer=gateway

POST /pedidos                          pedro-test-server
└─ pedido.Service.Create                 app.layer=service
   └─ queue.Publish                      app.layer=repository
      └─ pedido.JobHandler.Handle   pedro-test-worker   app.layer=handler   (mesmo trace_id)
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
| `make test` | unitários (services + lógica em `entities`/`structs`), com race detector |
| `make coverage` | cobertura de `services/`, falha abaixo de 80% |
| `make lint` | `golangci-lint` — é ele que cobra as regras de camada |
| `make test-e2e` | fluxos de ponta a ponta com testcontainers (Postgres, Redis, LocalStack, Keycloak) |
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

### Fora do Docker

Com Postgres, Redis, LocalStack e Keycloak no ar:

```bash
cp .env.example .env
go install github.com/pressly/goose/v3/cmd/goose@v3.28.0
make migrate-up
make run-server   # noutro terminal: make run-worker
```

---

## Estrutura

```
app/
├── cmd/{server,worker}/    o que cada processo monta
├── src/
│   ├── interfaces/<dom>/   os contratos. Folha: as três camadas apontam para cá.
│   ├── handlers/<dom>/     entrega: rotas HTTP (com as anotações da doc) e fila
│   ├── services/<dom>/     regra de negócio
│   ├── repositories/<dom>/ adapters de I/O (pgx, Redis, SQS)
│   ├── entities/ structs/  domínio e o que atravessa camadas
│   └── libs/               config, observabilidade, middleware, auth, runtime da fila
└── test/e2e/               testcontainers: fluxos de ponta a ponta
docker/                     Dockerfile dos apps e do goose, realm, init do LocalStack
infra/                      Terraform (sandbox e prod)
migrations/                 SQL do goose
```

Domínio novo = uma pasta em cada camada que ele toca, mais a linha no `module.go` da camada e
no `main` do processo. As convenções estão em [CLAUDE.md §3](CLAUDE.md#3-camadas).
