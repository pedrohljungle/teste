# CLAUDE.md — regras deste repositório

Contrato de como escrever código aqui. O **porquê** de cada decisão está em
[ARCHITECTURE.md](ARCHITECTURE.md); este arquivo é a regra em si.

Quem cobra não é review: é `make lint` (`.golangci.yml`), `make test` e `make test-e2e`. Antes
de dizer que terminou, os três têm que passar.

---

## 1. Idioma

- **Código e comentários em inglês.** Identificador, arquivo, pacote, mensagem de erro, nome
  de teste e mensagem de falha de teste: tudo em inglês.
- Documentação em Markdown (`*.md`) continua em português.
- Esta regra vale **só neste projeto**: o padrão do time manda comentar em pt-BR, aqui não.

## 2. Comentário

- **GoDoc em todo identificador exportado** — uma frase, começando pelo nome.
- Fora isso, comente **só fluxo fora do padrão**: a decisão que um leitor competente não
  deduziria do código (por que engolir este erro, por que este contexto não herda
  cancelamento).
- **Não** narrar o que o código já diz.

## 3. Camadas

Três camadas, e só elas, em `app/src/`:

| Camada | Faz | Não faz |
|---|---|---|
| `interfaces/` | os **contratos** de cada domínio: o que ele oferece, o que precisa, e os erros | qualquer comportamento |
| `handlers/` | recebe (HTTP **ou** mensagem de fila), valida a borda, chama o service, responde | regra de negócio, I/O de dado |
| `services/` | regra de negócio, implementando os contratos | conhecer pgx, echo, SDK, IDP |
| `repositories/` | adapters de I/O (Postgres, fila) que implementam os contratos | regra de negócio, HTTP |

Mais dois pacotes-folha: `entities/` (espelho de tabela) e `structs/` (o que atravessa camada
— dado, mais a lógica trivial sobre esse dado).

**Todo o resto vai em `app/src/libs/`**: `config`, `db`, `awsclients`, `observability`,
`auth`, `middleware`, `jobrunner`, `appinfo`, `bootstrap`. `libs/` é infraestrutura e runtime, nunca regra de
negócio.

**A dependência aponta para dentro:** `handlers → services → entities`. O service depende da
porta que ele declara, nunca do `repositories`.

### Dentro de cada camada: um módulo por domínio

A camada em si **não tem código** — só um `module.go` que agrega. O código mora num pacote por
domínio:

```
interfaces/          handlers/            services/            repositories/
└── <dominio>/       ├── module.go        ├── module.go        ├── module.go
    ├── service.go   ├── <dominio>/       └── <dominio>/       ├── <dominio>/
    ├── repository.go│   ├── module.go        ├── module.go    │   ├── module.go
    └── errors.go    │   ├── http.go          ├── service.go   │   └── postgres.go
                     │   └── job.go           └── *_test.go    └── queue/
                     ├── health/
                     └── identity/
```

**Hoje não há domínio nenhum**: `interfaces/`, `services/` e as pastas de domínio estão vazias
de propósito. Criar o primeiro é criar uma pasta em cada camada que ele toca, mais a linha no
`module.go` da camada e no `main` do processo.

- **Domínio novo = uma pasta em cada camada que ele toca**, com `module.go` próprio, somada à
  linha no `module.go` da camada.
- **O nome do pacote é o domínio** (`task`), não `taskservice`. Dentro dele, sem gaguejar:
  `task.Service`, `task.HTTPHandler`, `task.PostgresRepository`.
- Como o nome se repete entre camadas, quem importa mais de um **usa alias**: `tasksvc`,
  `taskrepo`, `taskhandler`.
- **Não é domínio, mas é da camada**: `repositories/queue/` (adapter
  genérico), `handlers/health` e `handlers/identity` (as duas rotas que todo serviço tem).
  Ficam ao lado dos domínios. O que não é de camada nenhuma — middleware — vai para `libs/`.
- **A amarração porta → adapter é do módulo do domínio.** Quando o adapter é genérico e não
  pode conhecer o domínio, a amarração sobe para o `module.go` da camada — é o caso da fila de dead letter.

### Toda fronteira entre camadas é uma interface

Nenhuma camada depende do tipo concreto da vizinha.

| Fronteira | Contrato | Declarado em |
|---|---|---|
| handler → service | `Service`, `CompletionService` | `interfaces/<dominio>` |
| service → banco/fila | `Repository`, `CompletionRepository`, `Queue` | `interfaces/<dominio>` |
| middleware → IDP | `auth.TokenVerifier` | `libs/auth` |
| runtime de fila → adapter/handler | `jobrunner.Source`, `jobrunner.Handler` | `libs/jobrunner` |

Regras:

- **Contrato de domínio mora em `interfaces/<dominio>`**, nunca dentro do service nem do
  adapter. Os sentinelas (`ErrNotFound`, `ErrAlreadyCompleted`, `ErrEmptyTitle`) vão junto:
  fazem parte do mesmo contrato.
- `interfaces/` é **folha**: importa `entities` e `structs`, mais nada. Nem `libs/`, nem
  driver, nem framework entra numa assinatura.
- **Handler não importa `services/`; repository não importa `services/`.** O lint cobra. Os
  dois dependem de `interfaces/`.
- O construtor devolve a **interface** (`func NewService(...) taskiface.Service`); a struct é
  minúscula. **É o `return` do construtor que afirma o contrato em tempo de compilação** — não
  existe `var _ Iface = (*impl)(nil)` neste repositório, nem em linha solta nem em bloco
  `var ( _ A = ...; _ B = ... )`. Uma struct que atende dois contratos ganha dois construtores,
  cada um devolvendo o seu (`NewService`, `NewReferenceResolver`).
- Quando o construtor precisa devolver o concreto (porque o tipo tem ciclo de vida próprio, como
  `auth.Verifier`), quem afirma é a **função de amarração no `module.go`**:
  `fx.Provide(func(v *Verifier) TokenVerifier { return v })`. O mesmo vale para adapter genérico
  que satisfaz contrato de domínio (`repositories/module.go`).
- Porta de runtime (`cronjob.Task`, `jobrunner.Source`) é afirmada **onde o processo é composto**:
  a chamada de registro em `app/cmd/<processo>/main.go`. Assim o handler não importa o runtime.
- **Dublê é struct que implementa a interface**, escrita no próprio arquivo de teste — sem
  framework de mock. Os testes de `services/task` são o exemplo.
- Alias de import quando dois pacotes homônimos se encontram: `taskiface`, `tasksvc`,
  `taskrepo`, `taskhandler`.

### Middleware

- Mora em **`libs/middleware`**, não em `handlers/`: ele não entrega nada, decide se a
  requisição chega a alguém.
- Aplicado de duas formas, e só duas: **`e.Use(...)`** para o que vale para toda requisição
  (`Recover`, `RequestID`, OTel, `Telemetry.TraceRequest`), e **na definição da rota** para o
  que é decisão daquela rota:

  ```go
  e.GET("/me", h.Me, requireAuthentication)
  e.GET("/relatorios", h.List, requireAuthentication, requireRole)
  ```

  **Nada de grupo com middleware embutido**: a rota tem que se explicar sozinha.
- O `ServerRoutes` de cada domínio **recebe o middleware por parâmetro**.
- Nome diz **o que é exigido**, não o que a função faz: `RequireAuthentication`,
  `RequireRealmRole`, `TraceRequest`, `AuthenticatedPrincipal`.

### Onde cada coisa vai

| Preciso adicionar… | Vai em… |
|---|---|
| Contrato novo (interface, sentinela) | `interfaces/<dominio>/` |
| Endpoint novo de um domínio | `handlers/<dominio>/http.go`, registrado no `ServerRoutes` dele |
| Consumo de um tipo novo de mensagem | `handlers/<dominio>/job.go`, registrado no `PrepareWorker` dele |
| Regra/decisão de negócio | `services/<dominio>/` |
| Ler ou gravar em banco, fila, object storage | `repositories/<dominio>/`, implementando uma porta do service |
| Adapter de I/O genérico, sem domínio | `repositories/<nome>/`, com a amarração no `module.go` da camada |
| Integração externa que não guarda dado (IDP, pagamento) | um adapter em `libs/`, atrás de uma interface |
| Client de SDK (SQS, S3) | `libs/awsclients` só constrói; quem usa é `repositories/` |
| Struct que só uma camada enxerga | na própria camada |
| Struct que atravessa camadas | `structs/` |

## 4. Server e worker: mesmo código, dois entrypoints

Este é o ponto mais importante do desenho, e ele copia o monolito do coruja.

**Cada domínio expõe duas funções de registro, e as `main` chamam:**

```go
// handlers/task/http.go
func ServerRoutes(g *echo.Group, h *HTTPHandler)

// handlers/task/job.go
func PrepareWorker(runner *jobrunner.Runner, source jobrunner.Source, h *JobHandler)
```

- **`app/cmd/server/main.go`** é o servidor inteiro: Echo, ordem dos middlewares, chamadas de
  `ServerRoutes` e ciclo de vida. **Não existe pacote de runtime HTTP** — se você está criando
  um, parou no lugar errado.
- **`app/cmd/worker/main.go`** é o consumidor: monta o `jobrunner`, chama os `PrepareWorker` e
  sobe o probe HTTP próprio (`/health`).
- Acrescentar domínio ao processo = **uma linha na `main`** e o arquivo no domínio.
- O worker **tem handler próprio** no mesmo domínio: consumir fila é entrega.
- **Contrato do handler de fila, e só ele:** devolveu `nil` → a mensagem é apagada do SQS;
  devolveu erro → **não** é apagada, volta a ficar visível quando o visibility timeout expira e
  é entregue de novo, até a DLQ. Não existe terceira saída.
- Disso decorrem duas obrigações: **todo handler é idempotente** (entrega é at-least-once), e
  **falha que retry não conserta devolve `nil`** — mensagem apontando para registro que não
  existe mais está concluída, não falhada.
- O handler do worker chama **um service próprio** (`task.CompletionService`), não o service do
  servidor. Regra diferente, service diferente.
- Tudo o que os dois compartilham está em `libs/bootstrap.Core`.

## 5. Doc de API (swag + Swagger UI)

- Rotas são **Echo puro**. A doc vem de **anotações swag** ao lado de cada handler, geradas em
  `app/docs` (commitado) e servidas pelo Swagger UI.
- **Rota nova = anotação nova.** Um cenário de e2e exige que toda rota registrada apareça no
  documento; sem a anotação, ele quebra.
- Anotação de swag é **a única exceção** à regra de comentário (§2): é metadado, não prosa.
- **Toda rota protegida declara duas coisas**: o middleware na rota e `@Security OAuth2Password`
  na anotação. O primeiro é o que a máquina cobra; o segundo é o cadeado na doc.
- **Resposta não é a entidade**: o handler tem o seu `Response`, mapeado da entity. O payload da
  API e a linha da tabela mudam por motivos diferentes.
- **Erro é `structs.APIError`** — o tipo que o `@Failure` referencia. Handler e middleware
  devolvem `echo.NewHTTPError(status, msg)`, que o Echo renderiza exatamente nessa forma.
- Exemplo e descrição vão nas **tags do tipo** (`example:`) e nas anotações, não em comentário.
- Regenerar: `make docs` (precisa do `swag`; `go install github.com/swaggo/swag/cmd/swag@v1.16.6`).
- `DOCS_ENABLED` liga `/docs` e `/swagger/*`. **Desligada, a rota não é registrada** (404, não
  403). Fica desligada em produção.
- A `tokenUrl` servida é reescrita de `Keycloak.PublicTokenURL()` — o endereço **externo**. O
  valor gravado pelo swag na geração quebraria o Authorize fora da rede.

## 6. Fila: SQS

- Fila é **SQS**. Não há cache: Redis foi retirado (ver TO DO no ARCHITECTURE.md).
- `libs/awsclients` constrói o client; `repositories/<dominio>/queue.go` é o adapter.
- O trace viaja em **message attributes** (prefixo `otel-`), não dentro do corpo: o corpo é o
  payload do domínio.
- **Ack explícito.** O `jobrunner` só apaga a mensagem quando o handler devolve `nil`; erro
  significa redelivery e, depois de `maxReceiveCount`, DLQ.
- Por isso **todo handler de job é idempotente**: a mesma mensagem pode chegar duas vezes.
- A fila e a DLQ são criadas pela infra (`docker/localstack/init-queues.sh` em dev), nunca pela
  aplicação.

## 7. Injeção de dependência (Uber fx)

- Todo pacote publica um `Module` em `module.go`.
- **`go.uber.org/fx` só pode ser importado em `module.go` e em `app/cmd/`.** O lint cobra.
- Ciclo de vida (abrir/fechar pool, subir/parar servidor, esperar mensagem em andamento) vai
  em `fx.Lifecycle`, nunca em `defer` no `main`.

## 8. Erros

- **Biblioteca única: a stdlib `errors`.** `github.com/pkg/errors` é proibido pelo lint.
- Embrulhar com `fmt.Errorf("context: %w", err)`. Sempre `%w`.
- **Comparar SEMPRE com `errors.Is` / `errors.As`.** Nunca `==`, nunca `err.Error() ==`,
  nunca `strings.Contains` na mensagem. O `errorlint` cobra.
- Erro de contrato entre camadas é **sentinela exportada** no pacote que declara a porta
  (ex.: `task.ErrNotFound`), para o chamador decidir por `errors.Is`.
- **Todo erro é registrado.** Ver seção 8.

## 9. Observabilidade

- Uma superfície só: `libs/observability.Observer`, injetado em **toda** camada.
- Toda operação abre span com `observability.Trace` (devolve valor e erro) ou `TraceErr` (só
  erro), que encerram o span com o erro que a função devolveu:

  ```go
  func (s *Service) List(ctx context.Context, page structs.Page) ([]entities.Task, error) {
      return observability.Trace(ctx, s.obs, observability.LayerService, "task.Service.List",
          func(ctx context.Context) ([]entities.Task, error) {
              return s.tasks.List(ctx, page)
          })
  }
  ```

  Campos do span vão depois da função (`..., observability.String("walletId", id))`).
- **Nunca use retorno nomeado**: `(outcome structs.WagerOutcome, err error)` não existe neste
  repositório. A função declara os tipos (`(structs.WagerOutcome, error)`) e devolve **variáveis
  locais**. Isso vale para toda função, com span ou sem.
- Quando a operação precisa fazer algo em volta do span — registrar o resultado, ou encerrar com
  um erro diferente do devolvido (a leitura que não acha nada não é falha do serviço) —, use
  `Start` e separe o corpo numa função interna, sem `defer` nem retorno nomeado:

  ```go
  ctx, end := s.obs.Start(ctx, observability.LayerService, "wagering.Service.Get")
  tx, err := s.get(ctx, providerID, id)
  end(expectedMissing(err))
  return tx, err
  ```
- Camadas: `LayerHandler`, `LayerService`, `LayerRepository`, `LayerGateway`.
- **Engolir erro só via `obs.Error(...)`.**
- **Nenhuma camada importa `zap` nem o SDK do OpenTelemetry** — só `libs/observability` e
  `libs/db`. Use `observability.Field` e `observability.String/Int64/Duration/...`.
- A aplicação exporta **OTLP direto** para o backend (`grafana/otel-lgtm` no compose). **Não há
  coletor.**
- `service.name` e o campo `app` do log saem do `appinfo.App` do entrypoint.

## 10. Postgres

- **pgx** (`pgxpool`), SQL escrito à mão. Sem ORM.
- Pool em `libs/db`; query só em `repositories/`.
- Scan com `pgx.CollectRows` / `pgx.CollectOneRow` + `pgx.RowToStructByName`; a `entities`
  carrega tag `db:"..."`.
- `pgx.ErrNoRows` vira a sentinela do contrato — quem chama não precisa conhecer pgx.

## 11. Autenticação

- Keycloak (OIDC). A aplicação **não** guarda senha nem sessão.
- Middleware de JWT: `libs/middleware.Auth.RequireAuthentication`, declarado **na rota**, junto
  com o `@Security` correspondente na anotação.
- As chaves do realm são carregadas **no start** (`fx.Lifecycle.OnStart`, com retry).
- `KEYCLOAK_ISSUER` (endereço externo, o do claim `iss`) e `KEYCLOAK_INTERNAL_URL` (por onde a
  aplicação alcança o IDP) são campos diferentes. Não misturar.
- Papel na borda: `RequireRealmRole("algum-papel")`, também na rota. Existe e não tem uso hoje —
  nenhuma rota do scaffold exige papel. Autorização que precisa olhar o dado é regra de
  negócio e vai no service.

## 12. Migrações

- **goose CLI**, sempre. Nada de migração por código da aplicação.
- `make migrate-create NAME=...`, `make migrate-up`, `make migrate-status`.
- No compose, o serviço `migrate` roda o goose e os apps só sobem depois que ele termina.

## 13. Testes

Duas categorias, e só duas.

**Unitário** (`make test`) — roda em segundos, sem Docker:

- `services/<dominio>/` — **obrigatório**. Dublê das portas; nenhum teste sobe banco ou fila.
- `entities/` e `structs/` — **só onde existe lógica** (`Task.IsCompleted`,
  `Principal.HasRole`). Struct sem comportamento não ganha teste.
- **Em mais nenhum lugar.** O lint proíbe importar `testing` fora desses três. Se falta testar
  em handler, repositório ou `libs/`, ou a lógica está no lugar errado ou é caso de e2e.
- Piso de cobertura em `services/`: `make coverage` (mínimo 80%).

**Ponta a ponta** (`make test-e2e`) — em `app/test/e2e/`, com **testcontainers**:

- Sobe Postgres, LocalStack (SQS) e Keycloak de verdade, aplica as migrações e roda o
  servidor e o worker no mesmo processo.
- Valida **fluxo**, criando entidades: criar task pela API → job no SQS → worker conclui →
  linha no banco. Mais autenticação, papéis, paginação e idempotência de redelivery.
- Fica atrás da build tag `e2e`, para `make test` continuar rápido e sem Docker.
- Teste novo de fluxo vai aqui, não numa camada.

**Um arquivo = uma Feature, escrita em Gherkin no comentário.**

- O **cabeçalho do arquivo** traz `Feature:`, o papel/objetivo e a lista de cenários.
- **Cada função de teste** traz o seu `Scenario:` com `Given/When/Then`, e o corpo é esse
  cenário passo a passo. Se os dois divergirem, **o comentário é o bug**.
- Nome do teste é o cenário (`TestTaskCreatedThroughTheAPIIsCompletedByTheWorker`).
- Cenários distintos ficam em testes distintos, mesmo quando a asserção é parecida: se dois
  falham pelo mesmo motivo mas em camadas diferentes, juntar deixa um deles apodrecer.

**A máquina fica em `app/test/e2e/core/`** (containers, boot do fx, helpers HTTP/banco/fila).
O arquivo de teste não sobe container nem monta grafo — só cenário. `core` importa `app/src`,
os testes importam `core`, e `app/src` não sabe que isso existe: sem ciclo.

`make lint` roda duas vezes, a segunda com `--build-tags e2e`: código atrás de tag que
ninguém linta apodrece.

## 14. Infraestrutura

- Terraform em [`infra/`](infra/README.md). **O workspace é o ambiente** (`sandbox`, `prod`);
  o workspace `default` não é um ambiente e o plano falha nele.
- **Sizing vai em `locals.tf`**, indexado pelo workspace — não no `.tfvars`. No `.tfvars` fica
  o que difere por ambiente e não é tamanho (imagem, issuer, certificado, CIDR).
- **Regra de rede nomeia security group, nunca CIDR**, e mora toda em `security.tf`.
- **Só o ALB tem rota para a internet.** ECS e RDS ficam nas subnets privadas.
- **Segredo não passa pelo Terraform**: senha de banco é gerenciada pelo RDS no Secrets
  Manager e injetada na task como *secret*; o resto entra por ARN.
- Rodou `terraform fmt -recursive` e `terraform validate` antes de abrir PR.

## 15. Antes de terminar

```bash
make lint
make test
make coverage
make test-e2e
```

---

## Onde está o porquê

Este arquivo é a regra. A justificativa de cada uma está em
[ARCHITECTURE.md](ARCHITECTURE.md):

| Regra daqui | Decisão lá |
|---|---|
| 3 — Camadas, domínios, `interfaces/` | [§1 Hexagonal](ARCHITECTURE.md#1-o-paradigma-hexagonal-ports--adapters) |
| 3 — Middleware | [§1 Middleware](ARCHITECTURE.md#middleware-em-libs-nomeado-pelo-que-exige-declarado-na-rota) |
| 4 — Server e worker | [§7 Um source code, dois entrypoints](ARCHITECTURE.md#7-um-source-code-dois-entrypoints) |
| 5 — Doc de API | [§7 swag + Swagger UI](ARCHITECTURE.md#a-documentação-da-api-swag--swagger-ui) |
| 6 — Fila | [§9 SQS com ack explícito](ARCHITECTURE.md#9-fila-sqs-com-ack-explícito) |
| 7 — `fx` | [§6 Uber fx](ARCHITECTURE.md#6-uber-fx-injeção-de-dependência-e-ciclo-de-vida) |
| 8 — Erros | [§12 Erros](ARCHITECTURE.md#12-erros-uma-biblioteca-e-errorsiserrorsas-sempre) |
| 9 — Observabilidade | [§11 OpenTelemetry + zap](ARCHITECTURE.md#11-observabilidade-opentelemetry--zap) |
| 10 — Postgres | [§10 pgx](ARCHITECTURE.md#10-postgres-com-pgx) |
| 11 — Autenticação | [§8 Keycloak](ARCHITECTURE.md#8-keycloak-como-idp) |
| 12 — Migrações | [§13 goose](ARCHITECTURE.md#13-migrações-com-goose) |
| 13 — Testes | [§14 Testes](ARCHITECTURE.md#14-testes) |
| 14 — Infraestrutura | [§19 Terraform](ARCHITECTURE.md#19-infraestrutura-terraform-em-dois-workspaces) · [§20 Cloudflare/WAF](ARCHITECTURE.md#20-borda-cloudflare-com-waf-na-frente-do-alb) · [§21 TO DO CI/CD](ARCHITECTURE.md#21-to-do--entrega-contínua) |
