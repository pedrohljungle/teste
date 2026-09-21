# SPEC-claude.md — plano de ataque do desafio

Este documento é o **diagnóstico e o plano**: o que o repositório já resolve do
[SPEC.md](SPEC.md), o que falta, em que ordem construir e o que é preciso provar.

**As decisões técnicas e os diagramas não estão aqui.** Eles foram para o
[ARCHITECTURE.md](ARCHITECTURE.md), junto das decisões que já existiam:

| Procurando por | Está em |
|---|---|
| As seis operações, `Money`, máquina de estados, reversões, idempotência, concorrência, eventos | [ARCHITECTURE §2 — Lógica de negócio](ARCHITECTURE.md#2-lógica-de-negócio-carteira-aposta-e-reversão) |
| Diagrama ER, DDL, o critério entre constraint e validação em Go | [ARCHITECTURE §3 — Modelo de dados](ARCHITECTURE.md#3-modelo-de-dados) |
| Componentes, pastas, transação SQL, outbox, filas, contratos HTTP | [ARCHITECTURE §4 — Fluxo de uma operação](ARCHITECTURE.md#4-fluxo-de-uma-operação) |
| A regra de como escrever código aqui | [CLAUDE.md](CLAUDE.md) |

Aqui ficam três coisas:

1. **O que já está pronto** e é reaproveitado sem tocar (§1).
2. **O plano em 13 PRs**, com a definition of done de cada um (§3).
3. **Os 91 cenários de e2e** que o SPEC §13 exige, escritos em Gherkin (§3.2).

> Comparação com o `HUMAN_SPEC.md`: ele cobre o núcleo síncrono (regras por `kind`, lock
> pessimista, escrita atômica na outbox) e deixa de fora idempotência persistente, inbox/SQS,
> workers assíncronos, reconciliação e a bateria de testes. Este documento parte do que ele
> acertou e fecha o resto.

---

## 1. Estado atual: o que já está feito

O repositório hoje é um **esqueleto de runtime** — o domínio `task` foi removido e
`migrations/` está vazio. O que sobrou é justamente a infraestrutura que o SPEC exige, e ela
já está no lugar.

| Exigência do SPEC | Onde já está | Status |
|---|---|---|
| §4 Composição com Uber `fx`, `fx.Module`/`Provide`/`Invoke` | `libs/bootstrap.Core`, um `module.go` por pacote | **pronto** |
| §4 `fx.Lifecycle`: start validado, shutdown observável, fecha dependência na ordem | `libs/jobrunner` (espera jobs em voo no `OnStop`), `libs/db`, `libs/observability` | **pronto** |
| §4 Domínio independente de Fx/HTTP/SQS/driver | `interfaces/` folha + `.golangci.yml` (`services-without-infrastructure`, `interfaces-are-leaves`) | **pronto, e cobrado pelo lint** |
| §2 IdP externo OIDC, `client_credentials` para serviço | `libs/auth` (verifier + service account), realm em `docker/keycloak/` | **pronto** |
| §2 RS256 fixado, JWKS carregado no boot com retry, 503 enquanto não carregou | `libs/auth/verifier.go` | **pronto** |
| §2 Autorização de borda na rota | `middleware.RequireAuthentication` / `RequireRealmRole`, declarados na rota | **pronto** |
| §4/§10 SQS com ack explícito, DLQ, redrive, visibility timeout | `libs/jobrunner` + `repositories/queue/sqs.go` + `docker/localstack/init-queues.sh` (`maxReceiveCount=5`, `VisibilityTimeout=60`) | **pronto — falta FIFO** |
| §10 Remover a mensagem só depois do tratamento | `jobrunner` só chama `Ack` quando o handler devolve `nil` | **pronto** |
| §10 `SIGTERM`: parar de buscar e concluir o que está em voo | `jobrunner.Run` — o contexto da mensagem não herda o cancelamento do laço | **pronto** |
| §4 `pgx` com SQL explícito | `libs/db/postgres.go` (`pgxpool`), query só em `repositories/` | **pronto** |
| §4 Migrations versionadas com up/down | goose CLI + serviço `migrate` no compose (`service_completed_successfully`) | **pronto — sem conteúdo** |
| §12 Logs estruturados, correlação, erro registrado uma vez | `libs/observability.Observer` (OTel + zap), `trace_id`/`span_id` em toda linha | **pronto** |
| §12 Trace atravessando a fila | trace em message attributes (`otel-`) | **pronto** |
| §13 `go test -race`, cobertura com piso, e2e com testcontainers reais (Postgres, LocalStack, Keycloak) | `make test`, `make coverage`, `app/test/e2e/core/` | **máquina pronta, cenários a escrever** |
| §15 `docker compose up --build`, `.env.example`, Makefile | `docker-compose.yaml`, `Makefile` | **pronto** |
| §9 Health check | `handlers/health` | **parcial — falta separar `live`/`ready`** |
| Doc de API | swag + Swagger UI, `apierr.Error` | **pronto** |

**Tradução:** dos 100 pontos do SPEC, a infraestrutura que costuma consumir a maior parte do
tempo já está de pé. O que falta é **domínio** — e é onde estão 70 dos 100 pontos.

### O que falta, em uma lista

Nada disso existe hoje: `Money`, as cinco tabelas, o agregado `Wallet`, a máquina de estados
de `WagerTransaction`, idempotência persistente, inbox, publisher de outbox, worker de
referência pendente, reconciliação, os quatro eventos, os endpoints, filas FIFO e as métricas
de negócio.

---

## 2. As decisões de arquitetura

Estão no [ARCHITECTURE.md](ARCHITECTURE.md), nas seções §2, §3 e §4 — incluindo as quatro que
ajustam o que o CLAUDE.md diz hoje: `entities/` com comportamento, o service compartilhado
entre HTTP e SQS, o runtime `libs/cronjob` e a troca das filas para FIFO
([§4.1](ARCHITECTURE.md#41-o-que-o-domínio-muda-no-desenho)).

---

## 3. Plano de implementação

Ordenado por ponto por hora de trabalho. Cada linha é um PR.

| # | Entrega | Cenários (§3.2) | Fecha |
|---|---|---|---|
| 1 | `entities/money.go` + testes unitários | — (unitário) | tira o risco eliminatório de float |
| 2 | Migration única: 5 tabelas, unicidade, `balance >= 0`, triggers | F11 | §5.8, §6.4 |
| 3 | `entities/` com invariante, transição e reidratação + testes | — (unitário) | §6 |
| 4 | `persistence.UnitOfWork` + `db.Accessor` + repositórios pgx | — | §11 atomicidade |
| 5 | `POST /wallets` + `OPENING` + ledger + outbox no mesmo commit | F1 | primeiro fluxo ponta a ponta |
| 6 | `POST /wagering/transactions`: `BET`, `WIN`, `LOSS` + idempotência + hash | F2, F4, F10 | §9, 15 pts |
| 7 | Publisher de outbox + `libs/cronjob` + `wager-events.fifo` | F8 | §11 |
| 8 | Consumidor SQS + inbox + FIFO | F3, F4 | §10 |
| 9 | `REFUND`/`ROLLBACK` + cronjob de referência | F5, F6 | §7 |
| 10 | GETs, cursor, reconciliação | F12, F13 | §9 |
| 11 | Métricas e `/health/live` + `/health/ready` | F14 | §12, 5 pts |
| 12 | Os 8 cenários de concorrência e recuperação | F7, F9 | §13, 10 pts |
| 13 | `README.md` e `ARCHITECTURE.md` atualizados | — | §15, 5 pts |

O passo 1 vem antes do 2 de propósito: `Money` decide se a coluna é `BIGINT` ou `NUMERIC`, e
migration publicada não se reescreve.

### 3.1 Definition of done

Vale para **toda** etapa da tabela, sem exceção. A etapa só está pronta quando os seis passam:

```sh
gofmt -l app/                # tem que sair vazio — SPEC 15 exige codigo formatado
go vet ./...                 # tambem coberto pelo govet dentro do golangci-lint
make lint                    # golangci-lint duas vezes: ./... e --build-tags e2e
make test                    # unitarios, ja com -race e -failfast
make coverage                # piso sobre services/, onde a regra mora
make test-e2e                # a suite com testcontainers (Postgres, LocalStack, Keycloak)
```

E mais duas condições que comando nenhum verifica sozinho:

- **todo cenário de §3.2 associado à etapa existe, roda e passa.** Cenário listado e não
  escrito é etapa não entregue;
- **nenhum cenário com `t.Skip`**, `t.Parallel()` faltando onde o cenário exige paralelismo,
  ou asserção comentada.

> **`gofmt` já está coberto por `make lint`.** O `.golangci.yml` habilita `gofmt` e `goimports`
> em `formatters:`, e o `golangci-lint` os reporta como `File is not properly formatted
> (gofmt)` (verificado com um arquivo mal formatado). O `gofmt -l` na lista acima continua
> valendo por ser o comando que o SPEC §15 cita literalmente, mas não é um segundo mecanismo.
> Um alvo `make verify` que encadeia os seis passos deixa a DoD num comando só.

### 3.2 Cenários de e2e

Um arquivo por Feature, em `app/test/e2e/`, no formato que o CLAUDE.md §13 exige: `Feature:`
com papel e objetivo no cabeçalho do arquivo, e o `Scenario:` em `Given/When/Then` no comentário
de cada função, cujo nome **é** o cenário. Gherkin vai em **inglês**, como todo comentário e
nome de teste (CLAUDE.md §1); esta prosa continua em português.

Toda Feature que mexe em dinheiro termina com a asserção final do SPEC §13: **saldo armazenado
igual à soma de créditos menos débitos do ledger.** É um helper de `core/`, chamado no fim de
cada cenário financeiro, não um cenário separado.

#### F1 — `wallet_opening_test.go` (etapa 5)

```gherkin
Feature: Wallet opening
  As the internal wallet service, I want a wallet and its opening credit committed together,
  so that no player ever starts with an unaudited balance.

  Scenario: Opening with a positive balance creates the credit, the ledger and the events
    Given no wallet exists for the player and currency
    When an internal service posts /wallets with an initial balance of 1000.00 BRL
    Then the wallet is created at version 1 with balance 1000.00 BRL
    And a PROCESSED OPENING transaction exists carrying no provider metadata
    And exactly one CREDIT entry exists with balanceBefore 0.00 and balanceAfter 1000.00
    And WagerTransactionProcessed and WalletBalanceChanged are in the outbox

  Scenario: Opening with a zero balance creates no transaction and no ledger
    Given no wallet exists for the player and currency
    When an internal service posts /wallets with an initial balance of 0.00 BRL
    Then the wallet is created at version 1 with balance 0.00 BRL
    And no OPENING transaction, no ledger entry and no financial event exist

  Scenario: A second wallet for the same player and currency conflicts
    Given a wallet exists for the player in BRL
    When an internal service posts /wallets for the same player in BRL
    Then the response is 409 and only one wallet exists

  Scenario: A wallet for the same player in another currency is accepted
    Given a wallet exists for the player in BRL
    When an internal service posts /wallets for the same player in USD
    Then the response is 201 and the player holds two wallets

  Scenario: The OPENING kind submitted over the wagering API is refused
    Given a wallet exists with balance 100.00 BRL
    When a provider posts /wagering/transactions with kind OPENING
    Then the response is 400 with failureCode OPENING_NOT_ALLOWED
    And the balance, the version and the ledger are unchanged
```

#### F2 — `wagering_http_test.go` (etapa 6)

```gherkin
Feature: Submitting an operation over HTTP
  As a game provider, I want each operation applied exactly once with a truthful answer,
  so that my ledger and the wallet never disagree.

  Scenario: A bet is processed and debits the wallet
    Given a wallet with balance 1000.00 BRL at version 1
    When the provider posts a BET of 25.00 BRL with an idempotency key
    Then the response is 200 with status PROCESSED, balance 975.00 and idempotentReplay false
    And the wallet is at version 2
    And exactly one DEBIT entry exists with balanceBefore 1000.00 and balanceAfter 975.00

  Scenario: A bet without sufficient balance is rejected and moves nothing
    Given a wallet with balance 10.00 BRL at version 1
    When the provider posts a BET of 25.00 BRL
    Then the response is 422 with failureCode INSUFFICIENT_FUNDS
    And the transaction is stored as REJECTED
    And the balance, the version and the ledger are unchanged
    And WagerTransactionRejected is in the outbox

  Scenario: A win is processed and credits the wallet
    Given a wallet with balance 100.00 BRL
    When the provider posts a WIN of 40.00 BRL referencing a bet of the same round
    Then the balance is 140.00 BRL and exactly one CREDIT entry exists

  Scenario: A loss is processed without touching the balance, the version or the ledger
    Given a wallet with balance 100.00 BRL at version 3
    When the provider posts a LOSS with money 0.00 BRL
    Then the response is 200 with status PROCESSED
    And the balance stays 100.00 and the version stays 3
    And no ledger entry is created
    And WagerTransactionProcessed is in the outbox and WalletBalanceChanged is not

  Scenario: A loss carrying a non-zero amount is rejected
    When the provider posts a LOSS with money 5.00 BRL
    Then the response is 422 with failureCode INVALID_AMOUNT

  Scenario: A bet of zero is rejected
    When the provider posts a BET with money 0.00 BRL
    Then the response is 422 with failureCode INVALID_AMOUNT

  Scenario: A request without the Idempotency-Key header is rejected
    When the provider posts a BET with no Idempotency-Key header
    Then the response is 400 and nothing is persisted

  Scenario: An amount with more than two decimals is rejected without rounding
    When the provider posts a BET of 25.001 BRL
    Then the response is 400 and nothing is persisted

  Scenario: An operation in a currency other than the wallet is rejected
    Given a wallet in BRL
    When the provider posts a BET of 25.00 USD
    Then the response is 422 with failureCode CURRENCY_MISMATCH

  Scenario: An operation for an unknown wallet is rejected
    When the provider posts a BET for a wallet that does not exist
    Then the response is 422 with failureCode WALLET_NOT_FOUND
```

#### F3 — `wagering_sqs_test.go` (etapa 8)

```gherkin
Feature: Consuming an operation from SQS
  As the worker, I want a message applied once and removed only after its commit,
  so that at-least-once delivery never becomes at-least-once money.

  Scenario: A bet delivered over SQS is processed exactly once
    Given a wallet with balance 1000.00 BRL
    When a WagerTransactionRequested message carrying a BET of 25.00 BRL is published
    Then the balance becomes 975.00 with exactly one DEBIT entry
    And the inbox holds the message id with a completion timestamp
    And the queue is empty

  Scenario: The same message delivered twice produces a single movement
    Given a bet already consumed from the queue
    When the very same message id is delivered again
    Then the inbox refuses it as a duplicate
    And no second ledger entry and no second event are created

  Scenario: A redelivery carrying a different body under the same message id is refused
    Given a bet already consumed from the queue
    When a message with the same id and a different payload hash arrives
    Then it is recorded as a conflict and applies nothing

  Scenario: The message is removed from the queue only after the commit
    Given a bet in flight
    When the commit has not happened yet
    Then the message is still invisible rather than deleted

  Scenario: A confirmed business rejection removes the message from the queue
    Given a wallet with balance 10.00 BRL
    When a BET of 25.00 BRL arrives over the queue
    Then the transaction is REJECTED, the message is deleted and the DLQ stays empty

  Scenario: A malformed body is recorded as failed and never retried
    When a message whose body is not valid JSON arrives
    Then it is recorded for audit and removed without redelivery

  Scenario: A transient database failure leaves the message for redelivery
    Given Postgres is unreachable
    When a bet arrives over the queue
    Then the message is not acked and reappears after the visibility timeout

  Scenario: A message exhausting maxReceiveCount lands in the DLQ
    Given a message that fails transiently on every attempt
    When it has been received more than maxReceiveCount times
    Then it is in the dead letter queue

  Scenario: The OPENING kind delivered over SQS is refused
    When a message carrying kind OPENING arrives
    Then it is rejected with OPENING_NOT_ALLOWED and no wallet is created
```

#### F4 — `idempotency_test.go` (etapas 6 e 8)

```gherkin
Feature: Idempotent replay across HTTP and SQS
  As a game provider retrying after a timeout, I want the stored outcome back,
  so that a retry can never move money twice.

  Scenario: A replay with the same key and the same payload returns the stored result
    Given a bet of 25.00 BRL already processed
    When the identical request is posted again
    Then the response is 200 with idempotentReplay true and the original transaction id
    And there is still exactly one ledger entry

  Scenario: A replay returns the balance observed at the original processing
    Given a bet of 25.00 BRL processed when the balance became 975.00
    And a later win that moved the balance to 1200.00
    When the original bet is replayed
    Then the response carries balance 975.00, not 1200.00

  Scenario: The same key with a different payload returns conflict
    Given a bet of 25.00 BRL already processed under a key
    When a bet of 30.00 BRL is posted under the same key
    Then the response is 409 and nothing is applied

  Scenario: The same operation under a different key returns conflict
    Given a bet already processed for a provider and external transaction id
    When the same pair is posted under a different idempotency key
    Then the response is 409 and nothing is applied

  Scenario: The same operation arriving over HTTP and over SQS is applied once
    When the identical operation is submitted over HTTP and published to the queue
    Then exactly one transaction, one ledger entry and one balance change exist

  Scenario: Idempotency survives a restart of every process
    Given a bet already processed
    When the server and the worker are restarted
    And the same request is posted again
    Then the response is still idempotentReplay true with the original result
```

#### F5 — `reversals_test.go` (etapa 9)

```gherkin
Feature: Refunds and rollbacks
  As the platform, I want a reversal to undo exactly its reference and only once,
  so that a returned bet can never be returned twice.

  Scenario: A refund of a processed bet credits the exact amount
    Given a processed BET of 25.00 BRL leaving the balance at 975.00
    When the provider posts a REFUND referencing it
    Then the balance returns to 1000.00 with a CREDIT entry of 25.00

  Scenario: A rollback of a bet credits the wallet
    Given a processed BET of 25.00 BRL
    When the provider posts a ROLLBACK referencing it
    Then the balance is credited by 25.00

  Scenario: A rollback of a win debits the wallet
    Given a processed WIN of 40.00 BRL
    When the provider posts a ROLLBACK referencing it
    Then the balance is debited by 40.00

  Scenario: A rollback of a refund debits the wallet
    Given a processed REFUND of 25.00 BRL
    When the provider posts a ROLLBACK referencing it
    Then the balance is debited by 25.00

  Scenario: A rollback that would overdraw is rejected with its own failure code
    Given a processed WIN of 40.00 BRL and a balance of 10.00 BRL
    When the provider posts a ROLLBACK referencing that win
    Then the response is 422 with failureCode ROLLBACK_INSUFFICIENT_FUNDS
    And the code differs from the one a bet without funds produces
    And the balance and the ledger are unchanged

  Scenario: A second reversal of the same reference is rejected
    Given a bet already refunded
    When a second REFUND referencing the same bet arrives
    Then the response is 422 with failureCode REFERENCE_ALREADY_REVERSED
    And the balance is credited only once

  Scenario: A rollback of a bet that was already refunded is rejected
    Given a bet already refunded
    When a ROLLBACK referencing the same bet arrives
    Then it is rejected with REFERENCE_ALREADY_REVERSED

  Scenario: A reversal disagreeing with its reference is rejected
    Given a processed bet of round-987
    When a REFUND referencing it declares round-988
    Then the response is 422 with failureCode REFERENCE_MISMATCH

  Scenario: A reversal whose amount differs from the reference is rejected
    Given a processed BET of 25.00 BRL
    When a REFUND of 20.00 BRL referencing it arrives
    Then the response is 422 with failureCode AMOUNT_MISMATCH

  Scenario: A reversal without a reference id is rejected
    When a REFUND arrives with no referenceExternalTransactionId
    Then the response is 400 and nothing is persisted

  Scenario: A reversal of a rejected reference is rejected
    Given a BET rejected for insufficient funds
    When a REFUND referencing it arrives
    Then the response is 422 with failureCode REFERENCE_NOT_PROCESSED
```

#### F6 — `pending_reference_test.go` (etapa 9)

```gherkin
Feature: Operations waiting for a reference
  As the platform, I want a reversal that arrives early to wait durably,
  so that out-of-order delivery costs nothing.

  Scenario: A refund arriving before its bet is held as pending reference
    Given no bet exists for the referenced external id
    When the provider posts a REFUND referencing it
    Then the response is 202 with status PENDING_REFERENCE
    And WagerTransactionPendingReference is in the outbox
    And the balance and the ledger are unchanged

  Scenario: The inbox message of a pending reference is completed once the pendency is durable
    Given a refund delivered over SQS whose reference is missing
    When the pendency has been committed
    Then the message is removed from the queue and the cronjob owns the continuation

  Scenario: A pending reference is resolved when its bet finally arrives
    Given a refund held as PENDING_REFERENCE
    When the referenced bet is processed
    And the reference cronjob ticks
    Then the refund becomes PROCESSED and credits the wallet exactly once

  Scenario: A pending reference expires and is rejected
    Given a refund held as PENDING_REFERENCE whose TTL has passed
    When the reference cronjob ticks
    Then it becomes REJECTED with failureCode REFERENCE_NOT_FOUND
    And WagerTransactionRejected is in the outbox

  Scenario: A reference that is itself pending keeps the reversal waiting
    Given the referenced transaction is PENDING_REFERENCE
    When the reference cronjob ticks
    Then the reversal stays PENDING_REFERENCE and its attempt counter grows

  Scenario: The retry backoff of a pending reference survives a restart
    Given a refund held as PENDING_REFERENCE with attempts already recorded
    When the worker is restarted
    Then the attempt counter and the next attempt time are the persisted ones
    And the resolution still happens once the reference arrives
```

#### F7 — `concurrency_test.go` (etapa 12)

```gherkin
Feature: Concurrent writers on one wallet
  As the platform, I want per-wallet coordination without a global lock,
  so that money stays correct while unrelated wallets keep running in parallel.

  Scenario: Fifty parallel submissions of the same bet produce a single debit
    Given a wallet with balance 1000.00 BRL
    When the same bet is submitted fifty times in parallel
    Then exactly one transaction and one ledger entry exist
    And the balance is 975.00 BRL

  Scenario: Two competing bets of eighty over one hundred leave twenty
    Given a wallet with balance 100.00 BRL
    When two distinct bets of 80.00 BRL are submitted at the same time
    Then one is PROCESSED and one is REJECTED with INSUFFICIENT_FUNDS
    And the balance is 20.00 BRL with exactly one DEBIT entry
    And resubmitting both changes nothing

  Scenario: Distinct wallets are processed in parallel
    When operations on many distinct wallets are submitted at the same time
    Then every one of them is processed
    And no wallet waited on another

  Scenario: The eighty-eighty race holds across three independent instances
    Given three instances with their own pools and memory
    When each receives one of two competing bets of 80.00 BRL over 100.00
    Then the outcome is one processed, one rejected and a balance of 20.00

  Scenario: Concurrent HTTP and SQS submissions of one operation apply it once
    When the same operation is posted over HTTP and published to the queue at the same time
    Then exactly one ledger entry exists and both callers see a consistent outcome
```

#### F8 — `outbox_test.go` (etapa 7)

```gherkin
Feature: Transactional outbox publication
  As a downstream consumer, I want every committed event and never an uncommitted one,
  so that what I read always happened.

  Scenario: An event is published only after its transaction commits
    Given a bet whose transaction has not committed
    Then nothing is on the outbound queue
    When the transaction commits and the publisher ticks
    Then the event is on the outbound queue and the row is PUBLISHED

  Scenario: An event whose transaction rolled back is never published
    Given a bet whose transaction failed after writing the outbox row
    Then neither the row nor the message exists

  Scenario: Two publishers competing over the same outbox publish each event once
    Given many pending outbox rows
    When two publishers tick at the same time
    Then every event is published exactly once and none is skipped

  Scenario: A publisher interrupted between publishing and confirming republishes the same event id
    Given a publisher killed after SendMessage and before marking the row
    When another publisher takes the row over
    Then the event is republished carrying the same eventId

  Scenario: An abandoned lock is reclaimed by another publisher
    Given a row locked by a publisher that died
    When the lease expires and another publisher ticks
    Then the row is published and leaves PENDING

  Scenario: A failed publication is retried with backoff
    Given the outbound queue is unavailable
    When the publisher ticks
    Then the row stays PENDING with a grown attempt count and a later next attempt

  Scenario: The outbox payload is an immutable snapshot
    Given a WalletBalanceChanged event written for a balance of 975.00
    When later operations move the balance
    Then the stored payload still reads 975.00 with the version it had
```

#### F9 — `recovery_test.go` (etapa 12)

```gherkin
Feature: Recovery after interruption
  As an operator, I want a killed process to cost nothing but a retry,
  so that no restart invents or loses money.

  Scenario: A consumer killed after the commit and before the ack does not apply twice
    Given a bet consumed and committed
    When the worker is killed before deleting the message
    And the message is redelivered to another instance
    Then the inbox refuses it and no second ledger entry exists

  Scenario: A pending transaction is resumed by another instance
    Given a transaction committed as PENDING by an instance that then died
    When another instance sweeps the pending work
    Then the transaction reaches a terminal state exactly once

  Scenario: A restart preserves idempotency, pendencies and financial consistency
    Given a mixed workload of processed, rejected and pending-reference operations
    When every process is restarted
    Then replays still return the original results
    And the pendencies still resolve
    And every wallet balance still equals its ledger sum

  Scenario: SIGTERM stops fetching and finishes the message in flight
    Given a message being processed
    When the worker receives SIGTERM
    Then it stops polling, finishes that message within the deadline and exits cleanly
```

#### F10 — `authorization_test.go` (etapas 5 e 6)

```gherkin
Feature: Authentication and provider isolation
  As the platform, I want identity to decide what a caller may touch,
  so that a provider can neither move nor read what is not theirs.

  Scenario: A request without a token is rejected and changes nothing
    When a bet is posted with no Authorization header
    Then the response is 401 and no transaction, ledger entry or event exists

  Scenario: A request with a forged token is rejected
    When a bet is posted with a token signed by another key
    Then the response is 401 and nothing is persisted

  Scenario: A request with an expired token is rejected
    When a bet is posted with an expired token
    Then the response is 401 and nothing is persisted

  Scenario: A provider cannot submit an operation for another provider
    Given a token issued for provider-a
    When a bet declaring provider-b is posted
    Then the response is 403 and nothing is persisted

  Scenario: A provider cannot read another provider's transaction
    Given a transaction belonging to provider-b
    When provider-a fetches it by id
    Then the response is 404 and no data about it is exposed

  Scenario: A provider cannot replay another provider's operation
    Given an operation processed for provider-b
    When provider-a posts it with the same idempotency key
    Then the response is 403 and the stored result is not disclosed

  Scenario: Opening a wallet requires the internal service role
    When a provider token posts /wallets
    Then the response is 403 and no wallet is created

  Scenario: The health endpoints are reachable without a token
    When /health/live and /health/ready are requested with no token
    Then both answer without authentication
```

#### F11 — `ledger_and_schema_test.go` (etapa 2)

```gherkin
Feature: Ledger integrity and schema guarantees
  As an auditor, I want the database itself to refuse a corrupt write,
  so that a bug in Go cannot produce an unauditable ledger.

  Scenario: A ledger row cannot be updated
    Given a ledger entry
    When an UPDATE is issued against it directly in SQL
    Then the database refuses it and the row is unchanged

  Scenario: A ledger row cannot be deleted
    When a DELETE is issued against a ledger entry directly in SQL
    Then the database refuses it and the row is still there

  Scenario: A balance cannot be driven negative through the schema
    When an UPDATE sets a wallet balance below zero directly in SQL
    Then the check constraint refuses it

  Scenario: A second ledger entry for the same transaction is refused
    When two entries are inserted for the same wallet and transaction
    Then the unique constraint refuses the second

  Scenario: A second opening for the same wallet is refused
    When a second OPENING is inserted for a wallet
    Then the partial unique index refuses it

  Scenario: A second successful reversal of one reference is refused
    When two PROCESSED reversals are inserted for the same reference
    Then the partial unique index refuses the second

  Scenario: Migrations apply and roll back cleanly
    When every migration is applied and then rolled back
    Then the schema returns to its previous state with no error
```

#### F12 — `queries_test.go` (etapa 10)

```gherkin
Feature: Reading wallets, ledger and transactions
  As a provider, I want to follow an operation and page a ledger reliably,
  so that I can reconcile on my side.

  Scenario: The ledger is paginated by an opaque cursor with stable ordering
    Given a wallet with more entries than one page
    When the pages are walked with the returned cursor
    Then every entry appears exactly once in a stable order

  Scenario: Pagination stays stable while new entries arrive
    Given a page already fetched
    When new entries are written and the next page is requested
    Then no entry from the first page is repeated or skipped

  Scenario: An invalid cursor is rejected
    When the ledger is requested with a malformed cursor
    Then the response is 400

  Scenario: A transaction query exposes its pending state and failure code
    Given one PENDING_REFERENCE and one REJECTED transaction
    When each is fetched by id
    Then the status and, for the rejected one, the failureCode are visible

  Scenario: A transaction can be fetched by provider and external id
    Given a processed operation
    When it is fetched by provider id and external transaction id
    Then the same transaction comes back
```

#### F13 — `reconciliation_test.go` (etapa 10)

```gherkin
Feature: Wallet reconciliation
  As an operator, I want the stored balance checked against the ledger without touching it,
  so that a divergence is found rather than hidden.

  Scenario: Reconciling a consistent wallet reports no difference
    Given a wallet opened with 1000.00 BRL and one bet of 25.00 BRL
    When reconciliation is requested
    Then storedBalance and calculatedBalance are 975.00, difference is 0.00
    And consistent is true and checkedEntries is 2

  Scenario: Reconciliation reports an injected divergence and changes nothing
    Given a wallet whose stored balance was tampered with directly in SQL
    When reconciliation is requested
    Then consistent is false with the exact difference
    And the balance is left untouched
    And the divergence is logged and counted in the metric

  Scenario: Reconciliation is consistent under concurrent movements
    Given operations running against the wallet
    When reconciliation is requested
    Then the two balances come from the same snapshot and never disagree spuriously
```

#### F14 — `lifecycle_and_health_test.go` (etapa 11)

```gherkin
Feature: Composition and lifecycle
  As an operator, I want boot and shutdown to be observable and complete,
  so that a deploy neither starts broken nor stops halfway.

  Scenario: The fx graph validates for both entrypoints
    When the server and the worker graphs are validated
    Then no port is missing an adapter and no constructor runs

  Scenario: The application starts and stops releasing every resource
    When the application starts and is then stopped
    Then the pool, the clients and the workers are closed in order with no leak

  Scenario: Readiness fails while Postgres is unavailable
    Given Postgres is down
    When /health/ready is requested
    Then it answers 503 while /health/live still answers 200

  Scenario: Readiness fails while SQS is unavailable
    Given the queue is unreachable
    When /health/ready is requested
    Then it answers 503

  Scenario: Boot fails loudly when the IdP is unreachable
    Given Keycloak is down
    When the server starts
    Then the boot fails after its retries instead of serving unauthenticated traffic
```

---

---

## 4. O que fica de fora, declarado

O SPEC §15 pede limitações explícitas.

- **Partidas dobradas** — opcional no SPEC; o ledger de uma perna cobre a auditoria pedida.
- **Reversão parcial** — fora do escopo por definição do SPEC §7.
- **Multi-moeda em operação** — o tipo carrega a moeda e há teste de incompatibilidade, mas os
  cenários principais rodam em BRL, como o SPEC permite.
- **Redis** — continua no projeto para cache de consulta. **Saldo não é cacheado**: a única
  fonte é a linha travada no Postgres, e um cache de saldo transformaria a reconciliação numa
  medida do cache.
- **Tracing e teste de carga** — diferenciais. O tracing já existe de graça pelo `Observer`;
  teste de carga fica fora.

---

## 5. Resultado da implementação

Os 13 passos foram entregues, um commit por passo, todos com a definition of done (§3.1)
verde: `gofmt`, `go vet`, `golangci-lint` (0 issues, duas vezes), testes unitários com `-race`,
cobertura de `services/` acima de 80% (≈89%) e a suíte e2e com Postgres, Redis, LocalStack e
Keycloak reais. Nenhum PR foi aberto e nenhum repositório remoto foi criado; o histórico é local.

O que mudou em relação ao plano, e por quê:

| Plano | Entregue | Por quê |
|---|---|---|
| 91 cenários | 149 funções de teste e2e, mais os unitários | os cenários do plano viraram mais de um teste quando falhariam por causas diferentes (CLAUDE.md §13); apareceram casos que o plano não previu (trigger de `TRUNCATE`, rejeição por `PLAYER_MISMATCH`, `INTERNAL_ERROR`) |
| F4 "idempotência sobrevive a restart" e F6 "backoff sobrevive a restart" | um cenário só, em F9, com carga mista | o mesmo restart prova as duas coisas: replay devolve o resultado original e a pendência mantém tentativas, próxima tentativa e expiração |
| "três processos" independentes | três instâncias no mesmo processo de teste, cada uma com pool, verificador e memória próprios; o SIGTERM usa o **binário do worker** como processo real | um processo de SO por instância custaria minutos de build e boot sem provar nada a mais sobre as travas, que estão no Postgres |
| Kill do consumidor entre commit e ack | falha injetada no `Ack` (`Faults.FailAcknowledging`) | não há como matar um processo exatamente nesse ponto; a falha injetada deixa o commit feito e a mensagem sem apagar, que é o estado que o kill deixaria |
| Leituras abertas | leituras de carteira só para `internal_service`; o provedor lê só as transações dele (404 para as de outro) | o SPEC não dá ao provedor acesso ao saldo |
| `Money` em `structs/` | `Money` em `entities/`; `structs.MoneyDTO` é só o DTO do fio | decisão do autor do repositório durante a implementação |
| `iso4217` de biblioteca | tabela ISO 4217 embutida em `entities/money.go` | nenhuma biblioteca confiável e mínima o bastante para justificar a dependência |

Limitações que continuam declaradas em §4. Um ponto que não é do código: `HUMAN_SPEC.md` foi
apagado do disco por outra pessoa durante o trabalho e a remoção ficou fora dos commits.
