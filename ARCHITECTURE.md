# ARCHITECTURE.md — decisões de arquitetura do `pedro-test`

Este documento explica **o que** foi decidido e **por que**. A regra em si, curta e sem
justificativa, está em [CLAUDE.md](CLAUDE.md).

---

## 1. O paradigma: Hexagonal (Ports & Adapters)

**Hexagonal (Ports & Adapters), na versão leve.** A regra de negócio fala com o mundo externo
só por **interfaces (ports) que ela mesma declara**, e os adapters (pgx, SQS, Keycloak,
Echo) as implementam. Não é Clean Architecture com `application/`, `domain/`, `usecases/`: é o
mínimo que entrega regra testável e fronteira clara, e que **cresce aprofundando o hexágono**
em vez de trocar de paradigma.

As três camadas:

```
handlers/       recebe e encaminha. Valida a borda, chama o service, responde. Sem regra.
services/       a regra de negócio. Declara as portas de que precisa.
repositories/   adapters de I/O (Postgres, fila) que implementam essas portas.
```

Mais dois pacotes-folha: `entities/` (espelho de tabela) e `structs/` (o que atravessa camada —
DTO, envelope de mensagem, principal, o modelo de erro da API). **`structs/` é dado**: regra
que viaja dentro de um DTO é regra aplicada em alguns caminhos e esquecida em outros.

**Este repositório não tem domínio nenhum.** `interfaces/`, `services/` e as pastas de domínio
das outras camadas estão vazias de propósito: o que está aqui é o esqueleto e a infraestrutura,
e o primeiro domínio real é criado por quem for usá-lo. O que sobrou de rota — `/health` e
`/me` — não é domínio: são as duas rotas que qualquer serviço tem, independentemente do que
ele faça.

Três consequências que não são óbvias:

1. **A regra vale para toda I/O com armazenamento externo, não só SQL.** Um `ReceiveMessage` no SQS
   (ou, se um dia houver, um `GET` num cache) segue o mesmo caminho que um `SELECT` no Postgres: moram em
   `repositories/`, atrás de uma porta declarada pelo service que as usa.
2. **Teste só onde a regra mora.** `services/`, mais a lógica que exista em `entities/` e
   `structs/`. Em `handlers/` e `repositories/` o lint **proíbe importar `testing`** — o que
   eles fariam de útil é fluxo, e fluxo é coberto de ponta a ponta em `app/test/`.
3. **O mecanismo, não só a regra.** O `.golangci.yml` transforma cada regra de camada em regra
   de `depguard`, e a complexidade ciclomática (`cyclop`) é cobrada **só nos handlers** —
   handler acima do limite é handler que ganhou regra de negócio.

### `interfaces/`: os contratos moram fora das implementações

Existe uma quarta pasta ao lado de `handlers/`, `services/` e `repositories/`: **`interfaces/`**,
com um pacote por domínio. Ela guarda o que o domínio **oferece**, o que ele **precisa** e os
**erros** que os dois lados comparam — e nada mais.

```
handlers/<dominio> ─┐
services/<dominio> ─┼─> interfaces/<dominio> ─> entities, structs
repositories/<dominio> ─┘
```

O motivo é que qualquer outro lugar cria um acoplamento:

- **Dentro do service**, a porta de I/O obrigaria o adapter a importar o service para
  satisfazê-la.
- **Dentro do adapter**, obrigaria o service a importar `repositories/` para enxergá-la — a
  dependência inverteria e a regra de negócio deixaria de ser testável sem banco.

Com um pacote-folha no meio, as três camadas apontam para o mesmo lugar e para mais nada.
`interfaces/<dominio>` importa `entities` e `structs`, e o lint proíbe qualquer coisa além disso —
nem `libs/`, nem driver, nem framework entra numa assinatura de contrato.

O efeito mais visível é no lint: **handler não pode importar `services/`** e **repository não
pode importar `services/`**. Os dois dependem de `interfaces/`, e a implementação concreta é
escolhida uma vez, pelo `fx`, na fiação.

Três convenções que caem disso:

- **O construtor devolve a interface e a struct é minúscula** (`func NewService(...) pedidoiface.Service`).
  "Aceite interfaces, devolva structs" é bom conselho geral e aqui não se aplica: a superfície
  inteira do pacote é o contrato, e devolver o concreto convidaria alguém a depender de um
  método que não faz parte dele.
- **Os sentinelas moram com as interfaces.** Quem depende só do contrato precisa distinguir as
  falhas, e faz isso com `errors.Is(err, pedidoiface.ErrNotFound)` — não lendo mensagem.
- **O contrato é afirmado em tempo de compilação, mas sem `var _ Iface = (*impl)(nil)`.** O que
  importa é o erro não nascer na fiação do `fx`, em runtime, com um texto que nomeia um módulo e
  não o método que mudou — e para isso basta o `return` do construtor, que já devolve a interface:
  mudar uma assinatura falha no `return`, apontando o método que falta. Um global só repetiria,
  numa linha solta, uma checagem que o construtor já faz.

  Onde não há esse `return` porque o construtor devolve o concreto — `auth.Verifier`, que tem
  ciclo de vida próprio, e o adapter genérico de fila que satisfaz um contrato de domínio —,
  quem afirma é a **função de amarração no `module.go`**
  (`fx.Provide(func(v *Verifier) TokenVerifier { return v })`), que é onde a decisão de ligar os
  dois mora. E uma porta de runtime (`cronjob.Task`) é afirmada pela chamada de registro na
  `main` do processo. Em todos os casos a checagem está **onde a ligação é feita**, e não numa
  declaração que só existe para ser verificada.

O resultado prático: a regra de negócio roda contra structs escritas no próprio arquivo de
teste — sem Postgres, sem SQS, sem Keycloak e sem framework de mock.

### Middleware: em `libs/`, nomeado pelo que exige, declarado na rota

Middleware não é entrega de nada: é encanamento que a entrega usa. Um handler responde uma
rota; um middleware decide se a requisição chega a algum handler. Por isso ele mora em
`libs/middleware` e não dentro de `handlers/`.

Ele é aplicado de duas formas, e só duas:

- **`e.Use(...)`** para o que vale para toda requisição: `Recover`, `RequestID`, o middleware do
  OpenTelemetry e o `Telemetry.TraceRequest`.
- **Na definição da rota**, para o que é decisão daquela rota:

  ```go
  e.GET("/me", h.Me, requireAuthentication)
  e.GET("/pedidos", h.List, requireAuthentication, auth.RequireRealmRole("vendas"))
  ```

Não há grupo com middleware embutido. A diferença não é estética: com grupo, descobrir se uma
rota é autenticada exige achar em qual grupo ela foi registrada, num arquivo que provavelmente
não é o que você está lendo. Com o middleware na rota, **a rota se explica sozinha** — e o
`ServerRoutes` de cada domínio recebe o middleware por parâmetro, então ler
`handlers/<dominio>/http.go` já diz que nada ali é público.

Os nomes seguem a mesma regra: dizem **o que é exigido**, não o que a função faz por dentro.
`Require` virou `RequireAuthentication`; `RequireRole` virou `RequireRealmRole` (papel de realm
do Keycloak, não papel de client); `Handle` virou `TraceRequest`; `PrincipalFrom` virou
`AuthenticatedPrincipal`.

### `libs/`: onde vai o que não é camada

Tudo o que não é `handlers`/`services`/`repositories`/`entities`/`structs` mora em
`app/src/libs/`: `config`, `db`, `awsclients`, `observability`, `auth`, `middleware`,
`jobrunner`, `appinfo`, `bootstrap`.

O motivo é de leitura: com dez pacotes soltos em `app/src/`, as três camadas deixam de saltar
aos olhos e a pergunta "onde isso vai?" volta a depender de quem revisa. Com `libs/`, `src/`
mostra **só** o desenho: cinco pastas de domínio e uma de infraestrutura.

### Um módulo por domínio dentro de cada camada

Cada camada é uma pasta **sem código**: só um `module.go` que agrega. O código vive num pacote
por domínio.

```
app/src/
├── handlers/                    ├── services/              ├── repositories/
│   ├── module.go                │   ├── module.go          │   ├── module.go
│   ├── <dominio>/               │   └── <dominio>/         │   ├── <dominio>/
│   │   ├── module.go            │       ├── module.go      │   │   ├── module.go
│   │   ├── http.go              │       ├── service.go     │   │   ├── postgres.go
│   │   └── job.go               │       ├── completion.go  │   │   └── queue.go
│   ├── health/                  │       └── *_test.go      │   └── queue/
│   └── identity/                                           │       └── sqs.go

interfaces/
└── <dominio>/
    ├── service.go      o que o domínio oferece
│   ├── repository.go   o que o domínio precisa
│   └── errors.go       os sentinelas do contrato
```

O motivo é o mesmo que levou `libs/` a existir: com tudo plano, `pedido_service.go`,
`pedido_completion_service.go` e `pedido_repository.go` ficam espalhados por três pastas, e a
pergunta "o que existe no domínio de pedidos?" exige percorrer as três. Com o
módulo por domínio, o domínio inteiro tem um endereço em cada camada, e **acrescentar um
domínio é acrescentar uma pasta em cada camada que ele toca** — não editar arquivos existentes.

Quatro convenções que caem disso:

1. **O nome do pacote é o domínio**, não o domínio mais a camada: `pedido`, não `pedidoservice`. A
   camada já está no caminho do import, e repeti-la no nome do tipo produziria
   `pedidoservice.PedidoService`. Dentro do pacote os nomes ficam curtos: `pedido.Service`,
   `pedido.Repository`, `pedido.HTTPHandler`, `pedido.PostgresRepository`.
2. **Quem importa mais de uma camada do mesmo domínio usa alias** (`pedidosvc`, `pedidorepo`,
   `pedidohandler`). São quatro arquivos no projeto inteiro, e o alias diz de qual camada o
   símbolo veio — que é justamente o que se quer saber ali.
3. **O que é da camada mas não é domínio fica ao lado dos domínios, nunca dentro de um**:
   `repositories/queue/` (adapter de SQS genérico). O que não é de camada nenhuma —
   middleware, por exemplo — vai para `libs/`.
4. **A amarração porta → adapter é do módulo do domínio.** `repositories/<dominio>/module.go`
   liga o `PostgresRepository` às portas que `interfaces/<dominio>` declara. A exceção é o
   adapter genérico: fazer `repositories/queue` importar um domínio só para satisfazer uma
   porta dele amarraria um adapter compartilhado ao primeiro que o usou — então essa amarração
   sobe para `repositories/module.go`, que é ponto de composição.

O que cada uma dessas peças é concretamente — qual biblioteca, qual banco, qual fila — está nas
seções seguintes, cada uma com o motivo da escolha.

---

## 2. Lógica de negócio: carteira, aposta e reversão

Esta seção e as duas seguintes descrevem o domínio: o que as operações fazem, como o
banco as guarda e por onde elas passam. O restante do documento é a infraestrutura que
as sustenta, e foi decidido antes delas.

### 2.1 As seis operações

O domínio tem uma carteira por `(playerId, currency)` e seis tipos de operação sobre ela. Cinco
chegam de fora, por HTTP ou SQS; `OPENING` é interno e nasce da abertura da carteira.

| Tipo | Origem | Movimenta | Valor | Ledger | Eventos |
|---|---|---|---|---|---|
| `OPENING` | interna | crédito | `> 0` | `CREDIT` | `Processed` + `BalanceChanged` |
| `BET` | externa | débito | `> 0` | `DEBIT` | `Processed` + `BalanceChanged` |
| `WIN` | externa | crédito | `> 0` | `CREDIT` | `Processed` + `BalanceChanged` |
| `LOSS` | externa | **nada** | `= 0.00` | **nenhum** | só `Processed` |
| `REFUND` | externa | crédito | `= o da BET` | `CREDIT` | `Processed` + `BalanceChanged` |
| `ROLLBACK` | externa | contrário ao original | `= o da referência` | inverso | `Processed` + `BalanceChanged` |

Três consequências que não são óbvias:

- **`LOSS` é o fim de rodada sem prêmio.** Ele exige `"0.00"`, não cria lançamento e **não
  incrementa a versão da carteira** — mas ainda exige a moeda da carteira e ainda produz
  `WagerTransactionProcessed`. Uma operação que não move dinheiro continua sendo um fato.
- **`ROLLBACK` é direcional.** Reverter uma `BET` credita; reverter um `WIN` ou um `REFUND`
  debita. É o único tipo cuja direção depende da referência, e não do próprio tipo.
- **Abertura com saldo zero não cria nada.** A carteira nasce na versão 1 com saldo `0.00`,
  sem `OPENING`, sem lançamento e sem evento financeiro. Só há o que auditar quando há dinheiro.

### 2.2 Dinheiro: `BIGINT` em unidades mínimas

**Decisão: `int64` em centavos, escala fixa de 2, moeda ISO 4217 em coluna própria.**

O SPEC §5.1 é eliminatório: dinheiro não passa por `float32`/`float64` em parsing, cálculo,
serialização ou persistência. `NUMERIC` no Postgres é exato, mas o caminho `NUMERIC` → Go é
onde o acidente acontece: um `Scan` para `float64` num campo esquecido não falha, só erra.
`BIGINT` → `int64` não tem esse caminho. O tipo que o banco guarda é o mesmo que o Go calcula.

- Limite: ±92.233.720.368.547.758,07 — sete ordens de grandeza acima de qualquer saldo real.
- **Overflow é verificado** em soma, subtração e negação (comparação antes da operação, não
  depois), e no parsing.
- Toda coluna monetária tem **sufixo `_minor`**, e toda tabela que guarda valor guarda também
  `currency`. Valor sem moeda não é dinheiro.
- O contrato externo é sempre string: `{"amount":"25.00","currency":"BRL"}`. A conversão
  string ↔ `int64` acontece uma vez, na borda.

Rejeitados no parsing, sem arredondamento silencioso: vazio, `NaN`, `Infinity`, notação
científica, mais de duas casas decimais, e negativo em entrada financeira externa. Negativo é
permitido em diferença interna (a reconciliação precisa dele).
### 2.3 Máquina de estados

```
                       ┌──────────────────────────────────────────┐
                       │                                          │
   (recebida)          ▼                                          │
       │          ┌─────────┐   referencia ausente   ┌────────────────────┐
       └─────────>│ PENDING │──────────────────────> │ PENDING_REFERENCE  │
                  └─────────┘                        └────────────────────┘
                    │  │  │                             │       │       │
      regra ok      │  │  │  falha permanente           │       │       │
        ┌───────────┘  │  └──────────┐       resolvida  │       │  TTL/tentativas
        ▼              ▼             ▼                  │       │  esgotados
  ┌───────────┐  ┌──────────┐  ┌────────┐               │       │
  │ PROCESSED │  │ REJECTED │  │ FAILED │<──────────────┘       │
  └───────────┘  └──────────┘  └────────┘                       │
        ▲              ▲                                        │
        └──────────────┴────────────────────────────────────────┘
        os tres sao terminais: nenhuma transicao sai daqui
```

| Estado | Entra quando | Sai para |
|---|---|---|
| `PENDING` | registro aceito, processamento não concluído | qualquer um dos outros |
| `PENDING_REFERENCE` | `REFUND`/`ROLLBACK` cuja referência ainda não chegou | `PROCESSED`, `REJECTED`, `FAILED` |
| `PROCESSED` | regra aplicada com sucesso | — terminal |
| `REJECTED` | recusa **definitiva** por regra de negócio | — terminal |
| `FAILED` | falha **permanente** de infraestrutura, registrada para auditoria | — terminal |

**Transitória × permanente.** É a distinção que decide entre retry e terminal:

- **Transitória** — Postgres indisponível, timeout, `serialization_failure`, throttling do SQS.
  A transação **não** é marcada; o erro sobe, o `jobrunner` não dá ack e a mensagem volta. No
  caminho HTTP vira `503`.
- **Permanente** — payload inválido que nenhuma repetição conserta, tipo desconhecido, moeda
  incompatível com a carteira. Vira `REJECTED` (regra de negócio) ou `FAILED` (infraestrutura
  que não se recupera, ex.: estouro de `int64` na persistência). Nos dois casos a mensagem sai
  da fila: repetir não muda o resultado.

**Retomada durável.** Operações sem dependência são concluídas **de forma síncrona**, em uma
transação — não existe commit intermediário de aceite, então não há `PENDING` órfão no caminho
feliz. `PENDING` permanece no schema como estado inicial do agregado e é varrido pelo worker
de retomada (`idx_wager_pending`), que cobre a interrupção entre o registro e a conclusão.
`PENDING_REFERENCE` é a pendência de verdade, e tem o seu próprio worker.
### 2.4 Referências e reversões

`REFUND` e `ROLLBACK` resolvem a referência por `(providerId, referenceExternalTransactionId)`.
O que cada um pode desfazer: `REFUND` só desfaz uma `BET`; `ROLLBACK` desfaz `BET`, `WIN` ou
`REFUND`. Os desfechos:

| Situação da referência | Desfecho |
|---|---|
| `PROCESSED` e compatível | aplica a reversão |
| não existe ainda | `PENDING_REFERENCE`, evento `WagerTransactionPendingReference` |
| ainda `PENDING`/`PENDING_REFERENCE` | `PENDING_REFERENCE` — mesma fila de retry |
| `REJECTED`/`FAILED` | `REJECTED` com `REFERENCE_NOT_PROCESSED`, **na hora**: não se espera por uma referência que nunca terá sucesso |
| divergente em provedor/jogador/carteira/moeda/rodada, ou de um tipo que a reversão não desfaz | `REJECTED` com `REFERENCE_MISMATCH` |
| valor diferente do referenciado | `REJECTED` com `AMOUNT_MISMATCH` (reversão parcial está fora do desafio) |
| já reversada com sucesso | `REJECTED` com `REFERENCE_ALREADY_REVERSED` |

As verificações do que já se sabe (tipo, provedor, jogador, carteira, moeda, rodada, valor) vêm
**antes** do estado da referência: uma reversão que discorda da sua referência é recusada mesmo
que a referência ainda esteja pendente.

**Onde a espera mora.** No próprio registro da transação: `reference_attempts`,
`reference_next_attempt_at` e `reference_expires_at`. Nada vive em memória, então qualquer instância
assume a espera exatamente de onde ela parou, inclusive depois de um reinício.

**O job de resolução** (`libs/cronjob`, em toda instância do worker) reivindica **uma reversão por
vez, uma unidade de trabalho cada**, com `FOR UPDATE SKIP LOCKED` na linha da transação. O lock é da
transação: não há lease a expirar, porque um worker que morre o solta morrendo. Dentro da unidade
de trabalho ele trava a carteira **antes** de ler a referência, como todo caminho de escrita, então
uma referência sendo processada na mesma carteira ou está inteira ali ou não está. Não há
inversão de lock com o caminho HTTP: o HTTP trava a carteira e insere uma linha nova, o job trava a
linha existente e depois a carteira.

**Quando a espera acaba.** Por `REFERENCE_TTL` (padrão 24 h) ou `REFERENCE_MAX_ATTEMPTS`
(padrão 12 novas tentativas), o que vier primeiro. Backoff exponencial de `REFERENCE_BACKOFF_BASE`
até `REFERENCE_BACKOFF_MAX`, com espalhamento de ±20%. Esgotada, a reversão vira `REJECTED` e emite
`WagerTransactionRejected`, e **o código diz o porquê**: `REFERENCE_NOT_FOUND` quando a referência
nunca chegou, `REFERENCE_NOT_PROCESSED` quando chegou e continua sem terminar.

**Cadeias.** Um `ROLLBACK` de um `REFUND` que ainda espera pela sua `BET` também espera. Chegando a
`BET`, o job resolve o `REFUND` e, no tick seguinte, o `ROLLBACK`.

**A mensagem de entrada é concluída assim que a pendência está persistida** (SPEC §6.5): o
job de resolução assume a continuidade, e segurar a mensagem na fila só faria a DLQ comer
uma operação que está progredindo.

**`REFUND` e `ROLLBACK` sobre a mesma aposta.** Decisão: **uma aposta recebe no máximo uma
reversão bem-sucedida, de qualquer tipo.** A segunda é rejeitada com
`REFERENCE_ALREADY_REVERSED`. O SPEC exige impedir duas reversões *do mesmo tipo*; escolher a
regra mais forte elimina a devolução dupla do mesmo débito sem depender de ordem de chegada — e
`uk_wager_single_reversal` a impõe no banco. Reverter uma reversão continua possível pelo
caminho legítimo: `ROLLBACK` apontando para o `REFUND`, que é outra referência.

### 2.5 Códigos de falha

Estáveis, documentados, e distinguindo entrada corrigível de resultado definitivo (SPEC §7).

| `failureCode` | Significado | Corrigível? |
|---|---|---|
| `INSUFFICIENT_FUNDS` | `BET` sem saldo | não |
| `ROLLBACK_INSUFFICIENT_FUNDS` | reversão que precisaria debitar mais que o saldo | não |
| `REFERENCE_NOT_FOUND` | referência não chegou dentro do TTL | não |
| `REFERENCE_NOT_PROCESSED` | referência existe mas terminou em `REJECTED`/`FAILED` | não |
| `REFERENCE_ALREADY_REVERSED` | a referência já recebeu uma reversão bem-sucedida | não |
| `REFERENCE_MISMATCH` | divergência de provedor, jogador, carteira, moeda ou rodada, ou uma referência de tipo que a reversão não desfaz (`REFUND` só desfaz `BET`; `ROLLBACK` desfaz `BET`, `WIN` ou `REFUND`) | não |
| `AMOUNT_MISMATCH` | valor da reversão diferente do referenciado | sim, com outro valor |
| `CURRENCY_MISMATCH` | moeda diferente da carteira | sim |
| `WALLET_NOT_FOUND` | carteira inexistente. **Não é gravada**: uma transação exige a carteira (chave estrangeira), então a rejeição sai só na resposta, `422` com o código | sim |
| `PLAYER_MISMATCH` | o `playerId` da operação não é o dono da carteira | sim |
| `OPENING_NOT_ALLOWED` | `OPENING` recebido por HTTP/SQS externo | não |
| `INVALID_AMOUNT` | valor viola a política do tipo | sim |
| `INTERNAL_ERROR` | código de uma transação `FAILED`: falha permanente de infraestrutura, só para auditoria | não |

Os dois primeiros são **códigos diferentes de propósito**: o SPEC §7 exige que a reversão sem
saldo não se confunda com a aposta sem saldo.
### 2.6 Idempotência

**Três camadas, e só a terceira é garantia.**

1. `MessageDeduplicationId` da FIFO — janela de 5 minutos, é otimização.
2. `inbox_messages` — dedup durável por consumidor.
3. **`uk_wager_provider_external` + `uk_wager_idempotency_key`** — a garantia financeira, que
   sobrevive a reinício de todos os processos (SPEC §5.2) e vale igual para HTTP e SQS.

**O hash do payload.** SHA-256 sobre JSON canônico (chaves ordenadas, sem espaço) dos campos
de negócio:

```
providerId · externalTransactionId · playerId · walletId · roundId · gameId
kind · money.amount · money.currency · referenceExternalTransactionId
```

Ficam **de fora**: a chave de idempotência e todo metadado de transporte (`messageId`,
`occurredAt`, header, envelope). Normalizações aplicadas **antes** do hash, e por isso
documentadas: `money.amount` para exatamente duas casas, `money.currency` para maiúscula,
`kind` para maiúscula, campo ausente omitido (nunca `null`). É a mesma função nos dois
caminhos de entrada — é o que faz a mesma operação por HTTP e por SQS colidir.

**As quatro respostas** (SPEC §9):

| Situação | Resposta |
|---|---|
| Chave nova | processa; `idempotentReplay: false` |
| Mesma chave, mesmo hash | devolve o resultado **persistido**; `idempotentReplay: true` |
| Mesma chave, hash diferente | `409` — a chave está comprometida, nada é aplicado |
| Mesmo `(providerId, externalTransactionId)`, chave diferente | `409` — a operação já existe |

O replay devolve `result_balance_minor` — o saldo **observado no processamento original**, não
o atual. É por isso que a coluna existe: sem ela, um replay depois de outras movimentações
mentiria sobre o resultado daquela operação.
### 2.7 Concorrência: lock pessimista por linha de carteira

**Decisão: `SELECT ... FOR UPDATE` na linha da carteira.**

O SPEC §8 aceita pessimista, otimista com retry ou atualização condicionada. Pessimista ganha
aqui porque o caso de contenção do desafio é **a mesma carteira sob disputa** (as duas apostas
de 80,00 sobre 100,00). Com controle otimista, esse cenário vira retry garantido: o segundo
escritor faz todo o trabalho para descobrir no `UPDATE` que perdeu. Com `FOR UPDATE`, ele
espera, lê o saldo já atualizado e decide **uma vez**, com o dado certo.

- **Lock por linha, nunca global** (SPEC §5.6): carteiras diferentes não se tocam. O
  `MessageGroupId` da FIFO é o `walletId`, então a fila preserva o mesmo particionamento —
  ordem por carteira, paralelismo entre carteiras.
- **`version` continua existindo** e é incrementada só quando o saldo muda (SPEC §6.2). Ela
  não é o mecanismo de concorrência; é o que o evento publica e o que um consumidor usa para
  ordenar.
- **Lost update é impossível** (SPEC §5.7): o segundo escritor só lê depois do commit do
  primeiro.
- **Deadlock:** uma transação trava **uma** carteira. Operação multi-carteira não existe neste
  desafio; se existir um dia, a regra é travar em ordem de `id`.
- **`uk_ledger_wallet_transaction` é a rede embaixo.** Se a regra falhar, o `INSERT` do
  segundo lançamento falha. Movimentação duplicada exigiria furar a regra **e** a constraint.

O cenário obrigatório do SPEC §8, passo a passo: duas apostas de 80,00 chegam em processos
diferentes; ambas disputam `FOR UPDATE`; a primeira debita e commita (saldo 20,00, versão 2, um
`DEBIT` no ledger); a segunda entra no lock, lê 20,00, rejeita com `INSUFFICIENT_FUNDS`, grava
a transação `REJECTED` **sem** lançamento. Reenvio de qualquer uma das duas cai na idempotência
e devolve o resultado gravado.
### 2.8 Eventos

Envelope único, tipo e versão definidos pelo construtor (SPEC §11), timestamps RFC 3339 UTC,
dinheiro em string decimal:

```json
{
  "eventId": "0192f2a1-...",
  "eventType": "WalletBalanceChanged",
  "aggregateId": "0192f291-...",
  "correlationId": "req-7f3a...",
  "causationId": "0192f298-...",
  "occurredAt": "2026-09-08T12:00:00.000Z",
  "version": 1,
  "data": { }
}
```

| Evento | Gatilho | `data` |
|---|---|---|
| `WagerTransactionProcessed` | conclusão com sucesso, **incluindo `LOSS`** | `transactionId` · `providerId` · `externalTransactionId` · `kind` · `money` · `walletId` |
| `WagerTransactionRejected` | rejeição definitiva | idem + `failureCode` |
| `WalletBalanceChanged` | alteração **efetiva** do saldo | `walletId` · `transactionId` · `direction` · `money` · `balanceBefore` · `balanceAfter` · `walletVersion` |
| `WagerTransactionPendingReference` | registro da espera | `transactionId` · `referenceExternalTransactionId` · `expiresAt` |

`LOSS` produz `WagerTransactionProcessed` e **não** produz `WalletBalanceChanged` — não houve
alteração de saldo. A abertura com saldo positivo produz os dois, no mesmo commit da carteira.
### 2.9 Observabilidade do domínio

A superfície é o `Observer` (`Start`, `Count`, `Measure`, `WithFields`). O domínio acrescenta o
vocabulário, e o e2e (`observability_test.go`) lê métricas e linhas de log em memória para provar
que cada uma é emitida.

**Campos de log** — `correlationId` (o request id no HTTP, o id da mensagem na fila),
`messageId`, `transactionId`, `walletId`, `providerId`. Vão no contexto (`WithFields`), então toda
linha escrita depois os carrega. Nunca o valor monetário, o token nem o segredo do client: os
cenários procuram esses valores em todas as linhas.

**Métricas**

| Métrica | Tags | Quando |
|---|---|---|
| `wager_transactions_total` | `kind`, `status`, `failure_code` (só em rejeição) | operação concluída |
| `wager_idempotent_replays_total` | `source` | repetição devolvida do que foi guardado |
| `wager_payload_conflicts_total` | `source` | mesma chave, conteúdo diferente |
| `wager_invalid_operations_total` | `source` | operação inválida na borda do service |
| `wager_unattached_rejections_total` | `source`, `failure_code` | rejeição sem transação gravada |
| `wager_concurrent_duplicates_total` | `source` | perdeu a corrida da unicidade e leu o vencedor |
| `wager_reference_retries_total` | — | nova tentativa de referência pendente |
| `wager_processing_duration_seconds` | `source`, `kind` | duração do processamento |
| `wallet_lock_wait_seconds` | — | espera pelo `FOR UPDATE` da carteira |
| `inbox_duplicates_total` | — | mensagem já vista |
| `sqs_message_retries_total` | — | mensagem devolvida à fila |
| `sqs_dead_letters_total` | `reason` | mensagem enviada à DLQ |
| `outbox_publish_attempts_total` | `result` | tentativa de publicar evento |
| `outbox_publish_delay_seconds` | — | do `occurredAt` até a publicação |
| `reconciliations_total` | `consistent` | conciliação executada |
| `reconciliation_divergences_total` | — | divergência encontrada |

**Health** — `/health/live` é o processo: responde 200 enquanto ele roda. `/health/ready`
verifica Postgres (ping) e SQS, e devolve 503 nomeando a dependência que falhou; viva não é pronta,
então uma instância sem banco sai da rotação em vez de ser reciclada. Ambos ficam fora da
autenticação, pelo motivo que a §8 já registra: probe que depende do IdP transforma queda do
Keycloak em reciclagem de tudo. O boot, por outro lado, **falha** quando o IdP não responde depois
das tentativas do verifier.

---

## 3. Modelo de dados

### 3.1 Diagrama

```
    ┌─ wallets ─────────────────────────────────────────────┐
    │ PK  id                                           UUID │
    │ UK  player_id                                    UUID │
    │ UK  currency                                  CHAR(3) │
    │     balance_minor                              BIGINT │
    │     version                                    BIGINT │
    │     created_at, updated_at                TIMESTAMPTZ │
    └───────────┬───────────────────────────────┬───────────┘
                │ 1                             │ 1
                │                               └───────────────────┐
                │ N   movimenta                                     │
                ▼                                                   │
    ┌─ wager_transactions ──────────────────────────────────┐       │
 ┌─▶│ PK  id                                           UUID │       │
 │  │     origin                                       TEXT │       │
 │  │     kind                                         TEXT │       │
 │  │     status                                       TEXT │       │
 │  │ FK  wallet_id                                    UUID │       │
 │  │     player_id                                    UUID │       │
 │  │     amount_minor                               BIGINT │       │
 │  │     currency                                  CHAR(3) │       │
 │  ├─ metadados externos: NULL quando origin = INTERNAL ───┤       │
 │  │     provider_id                                  TEXT │       │
 │  │ UK  external_transaction_id                      TEXT │       │
 │  │ UK  idempotency_key                              TEXT │       │
 │  │     payload_hash                                BYTEA │       │
 │  │     round_id                                     TEXT │       │
 │  │     game_id                                      TEXT │       │
 │  │     reference_external_transaction_id            TEXT │       │
 ┘──│ FK  reference_transaction_id                     UUID │       │
    ├─ resultado persistido: o replay le daqui ─────────────┤       │
    │     failure_code                                 TEXT │       │
    │     result_balance_minor                       BIGINT │       │
    │     result_wallet_version                      BIGINT │       │
    ├─ retomada da pendencia de referencia ─────────────────┤       │
    │     reference_attempts                            INT │       │
    │     reference_next_attempt_at             TIMESTAMPTZ │       │
    │     reference_expires_at                  TIMESTAMPTZ │       │
    ├─  ────────────────────────────────────────────────────┤       │
    │     created_at, updated_at                TIMESTAMPTZ │       │
    │     settled_at                            TIMESTAMPTZ │       │
    └─────────────────────────┬─────────────────────────────┘       │
                              │ 1                                   │
                              │                                     │
                              │ 0..1   gera                         │
                              ▼                                     │
    ┌─ wallet_ledger_entries ───────────────────────────────┐       │
    │ PK  id                                           UUID │       │
    │     seq                               BIGINT IDENTITY │       │
    │ FK  wallet_id                                    UUID │       │
    │ FK  transaction_id                               UUID │   N  registra
    │     direction                                    TEXT │  ◀────┘
    │     amount_minor                               BIGINT │
    │     currency                                  CHAR(3) │
    │     balance_before_minor                       BIGINT │
    │     balance_after_minor                        BIGINT │
    │     created_at                            TIMESTAMPTZ │
    └───────────────────────────────────────────────────────┘
```

```
kind     OPENING | BET | WIN | LOSS | REFUND | ROLLBACK
status   PENDING | PENDING_REFERENCE | PROCESSED | REJECTED | FAILED
origin   INTERNAL | EXTERNAL          direction   DEBIT | CREDIT
```

`LOSS` e operacao rejeitada nao geram lancamento — dai o `0..1`.

As duas tabelas de transporte nao tem FK para o dominio, de proposito: sao gravadas na mesma
transacao SQL, mas tem ciclo de vida e expurgo proprios.

```
    ┌─ inbox_messages ───────────────────────┐   ┌─ outbox_events ────────────────────────┐
    │ PK  consumer_name                 TEXT │   │ PK  event_id                      UUID │
    │ PK  message_id                    TEXT │   │     aggregate_type                TEXT │
    │     payload_hash                 BYTEA │   │     aggregate_id                  UUID │
    │     received_at            TIMESTAMPTZ │   │     event_type                    TEXT │
    │     completed_at           TIMESTAMPTZ │   │     event_version                  INT │
    └────────────────────────────────────────┘   │     correlation_id                TEXT │
      dedup duravel por consumidor               │     causation_id                  TEXT │
      (a janela de 5 min da FIFO nao basta)      │     payload                      JSONB │
                                                 │     occurred_at            TIMESTAMPTZ │
                                                 │     status                        TEXT │
                                                 │     attempts                       INT │
                                                 │     next_attempt_at        TIMESTAMPTZ │
                                                 │     locked_by                     TEXT │
                                                 │     locked_at              TIMESTAMPTZ │
                                                 │     published_at           TIMESTAMPTZ │
                                                 └────────────────────────────────────────┘
                                                   event_id e PK para sobreviver a
                                                   republicacao com o mesmo identificador
```
### 3.2 DDL

Só entra no schema o que o SPEC manda o schema impor. **Toda regra de negócio — política de
valor por tipo, aritmética do lançamento, metadado obrigatório por origem, enum válido — é
validada no domínio**, em `entities/`, e tem teste unitário. O critério de corte está em §3.3.

```sql
-- +goose Up

CREATE TABLE wallets (
    id            UUID        PRIMARY KEY,
    player_id     UUID        NOT NULL,
    currency      CHAR(3)     NOT NULL,
    balance_minor BIGINT      NOT NULL DEFAULT 0,
    version       BIGINT      NOT NULL DEFAULT 1,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at    TIMESTAMPTZ NOT NULL DEFAULT now(),

    -- SPEC 6.2: o par (playerId, currency) identifica uma unica carteira.
    CONSTRAINT uk_wallet_player_currency UNIQUE (player_id, currency),
    -- SPEC 5.8: "nao negatividade [deve ser] imposta pelo schema".
    CONSTRAINT ck_wallet_balance_non_negative CHECK (balance_minor >= 0)
);

CREATE TABLE wager_transactions (
    id           UUID        PRIMARY KEY,
    -- SPEC 6.3: "o schema deve distinguir operacoes internas e externas".
    origin       TEXT        NOT NULL,
    kind         TEXT        NOT NULL,
    status       TEXT        NOT NULL,

    wallet_id    UUID        NOT NULL REFERENCES wallets(id),
    player_id    UUID        NOT NULL,
    amount_minor BIGINT      NOT NULL,
    currency     CHAR(3)     NOT NULL,

    -- Metadados externos: o dominio os exige quando origin = 'EXTERNAL' e os recusa
    -- quando 'INTERNAL'. O schema so permite a ausencia.
    provider_id                       TEXT,
    external_transaction_id           TEXT,
    idempotency_key                   TEXT,
    payload_hash                      BYTEA,
    round_id                          TEXT,
    game_id                           TEXT,
    reference_external_transaction_id TEXT,
    reference_transaction_id          UUID REFERENCES wager_transactions(id),

    -- Resultado persistido: o replay le daqui, sem reaplicar a operacao.
    failure_code          TEXT,
    result_balance_minor  BIGINT,
    result_wallet_version BIGINT,

    -- Retomada da pendencia de referencia.
    reference_attempts        INT NOT NULL DEFAULT 0,
    reference_next_attempt_at TIMESTAMPTZ,
    reference_expires_at      TIMESTAMPTZ,

    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    settled_at TIMESTAMPTZ
);

-- SPEC 9: a operacao financeira e identificada por (providerId, externalTransactionId) e nao
-- pode ser reaplicada por outra chave.
CREATE UNIQUE INDEX uk_wager_provider_external
    ON wager_transactions (provider_id, external_transaction_id)
    WHERE origin = 'EXTERNAL';

-- SPEC 9: a chave recebida e guardada como veio; o servidor nao a substitui.
CREATE UNIQUE INDEX uk_wager_idempotency_key
    ON wager_transactions (provider_id, idempotency_key)
    WHERE origin = 'EXTERNAL';

-- SPEC 6.3: "impedir credito inicial duplicado".
CREATE UNIQUE INDEX uk_wager_single_opening
    ON wager_transactions (wallet_id)
    WHERE kind = 'OPENING';

-- SPEC 7: uma referencia nao recebe duas reversoes bem-sucedidas. O indice cobre REFUND e
-- ROLLBACK juntos, entao a combinacao dos dois sobre a mesma BET tambem e impedida (ver §2.4).
CREATE UNIQUE INDEX uk_wager_single_reversal
    ON wager_transactions (provider_id, reference_external_transaction_id)
    WHERE status = 'PROCESSED' AND kind IN ('REFUND','ROLLBACK');

-- Fila de trabalho dos workers: indice parcial, para o SELECT nao varrer a tabela.
CREATE INDEX idx_wager_pending_reference
    ON wager_transactions (reference_next_attempt_at)
    WHERE status = 'PENDING_REFERENCE';

CREATE INDEX idx_wager_pending
    ON wager_transactions (created_at)
    WHERE status = 'PENDING';

CREATE TABLE wallet_ledger_entries (
    id                   UUID        PRIMARY KEY,
    seq                  BIGINT      GENERATED ALWAYS AS IDENTITY,
    wallet_id            UUID        NOT NULL REFERENCES wallets(id),
    transaction_id       UUID        NOT NULL REFERENCES wager_transactions(id),
    direction            TEXT        NOT NULL,
    amount_minor         BIGINT      NOT NULL,
    currency             CHAR(3)     NOT NULL,
    balance_before_minor BIGINT      NOT NULL,
    balance_after_minor  BIGINT      NOT NULL,
    created_at           TIMESTAMPTZ NOT NULL DEFAULT now(),

    -- SPEC 6.4: "imponha no banco a unicidade de (walletId, transactionId)". E a barreira
    -- contra movimentacao duplicada: mesmo que dois processos passem pela regra em Go, o
    -- segundo INSERT falha.
    CONSTRAINT uk_ledger_wallet_transaction UNIQUE (wallet_id, transaction_id)
);

CREATE UNIQUE INDEX uk_ledger_seq ON wallet_ledger_entries (seq);
CREATE INDEX idx_ledger_wallet_cursor ON wallet_ledger_entries (wallet_id, seq);

-- SPEC 5.8 e 6.4: "imutabilidade do ledger [imposta] pelos mecanismos de protecao do banco" e
-- "a protecao contra edicao ou exclusao". Append-only nao pode depender da disciplina de quem
-- escreve o SQL.
-- +goose StatementBegin
CREATE FUNCTION ledger_is_append_only() RETURNS TRIGGER AS $$
BEGIN
    RAISE EXCEPTION 'wallet_ledger_entries is append-only: correction requires a new entry';
END;
$$ LANGUAGE plpgsql;
-- +goose StatementEnd

CREATE TRIGGER trg_ledger_no_update BEFORE UPDATE ON wallet_ledger_entries
    FOR EACH ROW EXECUTE FUNCTION ledger_is_append_only();
CREATE TRIGGER trg_ledger_no_delete BEFORE DELETE ON wallet_ledger_entries
    FOR EACH ROW EXECUTE FUNCTION ledger_is_append_only();
-- TRUNCATE is a deletion that no row trigger sees, and the REVOKE below does not bind the table
-- owner, so it gets its own statement trigger.
CREATE TRIGGER trg_ledger_no_truncate BEFORE TRUNCATE ON wallet_ledger_entries
    FOR EACH STATEMENT EXECUTE FUNCTION ledger_is_append_only();

-- Defesa em profundidade: o trigger cobre a linha, o REVOKE cobre o comando.
REVOKE UPDATE, DELETE, TRUNCATE ON wallet_ledger_entries FROM PUBLIC;

CREATE TABLE inbox_messages (
    consumer_name TEXT        NOT NULL,
    message_id    TEXT        NOT NULL,
    payload_hash  BYTEA       NOT NULL,
    received_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    completed_at  TIMESTAMPTZ,

    -- SPEC 6.5: "unicidade de (consumerName, messageId)".
    CONSTRAINT pk_inbox PRIMARY KEY (consumer_name, message_id)
);

CREATE TABLE outbox_events (
    event_id        UUID        PRIMARY KEY,
    aggregate_type  TEXT        NOT NULL,
    aggregate_id    UUID        NOT NULL,
    event_type      TEXT        NOT NULL,
    event_version   INT         NOT NULL,
    correlation_id  TEXT        NOT NULL,
    causation_id    TEXT,
    payload         JSONB       NOT NULL,
    occurred_at     TIMESTAMPTZ NOT NULL,
    status          TEXT        NOT NULL DEFAULT 'PENDING',
    attempts        INT         NOT NULL DEFAULT 0,
    next_attempt_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    locked_by       TEXT,
    locked_at       TIMESTAMPTZ,
    published_at    TIMESTAMPTZ
);

CREATE INDEX idx_outbox_due ON outbox_events (next_attempt_at, occurred_at)
    WHERE status = 'PENDING';
CREATE INDEX idx_outbox_aggregate ON outbox_events (aggregate_type, aggregate_id);

-- +goose Down
DROP TABLE outbox_events;
DROP TABLE inbox_messages;
DROP TRIGGER trg_ledger_no_truncate ON wallet_ledger_entries;
DROP TRIGGER trg_ledger_no_delete ON wallet_ledger_entries;
DROP TRIGGER trg_ledger_no_update ON wallet_ledger_entries;
DROP FUNCTION ledger_is_append_only();
DROP TABLE wallet_ledger_entries;
DROP TABLE wager_transactions;
DROP TABLE wallets;
```
### 3.3 O critério: o que é do banco e o que é do domínio

O SPEC nomeia, com essas palavras, o que o **schema** tem de impor:

> §5.8 — *"**Unicidade, não negatividade e imutabilidade do ledger** devem ser impostas pelo
> schema, pelas constraints e pelos mecanismos de proteção do banco."*
> §6.4 — *"**Imponha no banco** a unicidade de `(walletId, transactionId)` e a proteção contra
> edição ou exclusão."*
> §6.3 — *"**O schema deve** distinguir operações internas e externas e **impedir crédito
> inicial duplicado**."*

Essa lista é fechada, e o que está nela é o que sobra de garantia **mesmo que toda a regra em
Go falhe**:

| Invariante | Mecanismo | Exigido em |
|---|---|---|
| Carteira única por `(playerId, currency)` | `uk_wallet_player_currency` | §5.8 · §6.2 |
| Saldo nunca negativo | `ck_wallet_balance_non_negative` | §5.8 |
| Um lançamento por transação, por carteira | `uk_ledger_wallet_transaction` | §5.8 · §6.4 |
| Ledger não muda nem some | trigger `BEFORE UPDATE/DELETE` + `REVOKE` | §5.8 · §6.4 |
| Operação externa não se repete | `uk_wager_provider_external` | §5.8 · §9 |
| Chave de idempotência não se repete | `uk_wager_idempotency_key` | §5.2 · §9 |
| Origem interna distinguível da externa | coluna `origin` | §6.3 |
| Crédito de abertura não duplica | `uk_wager_single_opening` | §6.3 |
| Referência não recebe duas reversões | `uk_wager_single_reversal` | §7 |
| Mensagem não é tratada duas vezes | `pk_inbox` | §6.5 |

### O que **não** vai para o banco

O SPEC não pede validação de regra de negócio no schema — pede o contrário, em §6:
entidades com *"construtores com validação e métodos explícitos de transição"*, com as
invariantes preservadas *"em todas as operações públicas"*. Sobre a aritmética do lançamento
ele é literal: *"sua **construção** deve validar `balanceAfter = balanceBefore ± money`"* —
construção, não `CHECK`.

Então estas ficam em `entities/`, cada uma com teste unitário (que é onde o SPEC §13 as cobra):

| Regra | Onde | SPEC |
|---|---|---|
| `balanceAfter = balanceBefore ± money` | construtor de `LedgerEntry` | §6.4 |
| `LOSS` exige `0.00`; os demais exigem `> 0` | construtor de `WagerTransaction` | §7 |
| `REFUND`/`ROLLBACK` exigem referência | idem | §7 |
| `OPENING` é só interno; recusado por HTTP/SQS | borda + domínio | §6.3 |
| Metadado externo obrigatório por origem | construtor | §6.3 |
| Moeda ISO 4217 válida e compatível com a carteira | `Money` | §6.1 |
| `kind`, `status`, `origin`, `direction` válidos | tipos do domínio | §6.3 |
| Rejeição carrega `failureCode` | transição de estado | §7 |
| `version` só incrementa com mudança de saldo | agregado `Wallet` | §6.2 |

**O que se perde, declarado.** `ck_ledger_arithmetic` era a defesa mais barata contra um ledger
corrompido por um bug de cálculo, e §5.3 (*"as invariantes financeiras devem ser garantidas no
banco"*) daria base para mantê-la. Sem ela, quem garante a aritmética é o construtor mais o
teste unitário — e a reconciliação de §9 passa a ser o detector, não mais a segunda linha. É
uma constraint de uma linha se a decisão mudar.

---

## 4. Fluxo de uma operação

### 4.1 O que o domínio muda no desenho

Antes do schema, os pontos onde o SPEC pede algo diferente do que a regra do repositório diz
hoje. Todos cabem no paradigma; nenhum troca o paradigma.

**a) `entities/` deixa de ser espelho de tabela e ganha comportamento.**
O CLAUDE.md §3 descreve `entities/` como "espelho de tabela". O SPEC §6 exige agregado de
verdade: construtor com validação, **reidratação separada da criação** (reidratar não reaplica
movimentação nem emite evento), métodos explícitos de transição e invariante preservada em
toda operação pública. Isso **não** quebra nenhuma regra: `entities/` continua folha, sem
importar `libs/`, driver ou framework — o lint `entities-are-leaves` continua valendo — e o
CLAUDE.md §13 já permite teste unitário ali "onde existe lógica". É a §17 ("cresce sob demanda") funcionando como previsto.

O estado do agregado é **encapsulado** (campos não exportados), então a tag `db:"..."` não fica
no agregado e sim num **snapshot** exportado ao lado dele (`WalletSnapshot`,
`WagerTransactionSnapshot`, `LedgerEntrySnapshot`, `OutboxEventSnapshot`,
`InboxMessageSnapshot`). O repositório escaneia a linha no snapshot e chama
`Rehydrate<Agregado>`, que só valida que aquilo poderia existir — não reaplica movimentação,
transição nem evento. Para gravar, o caminho inverso é `agregado.Snapshot()`. As colunas que não
se aplicam a uma transação interna (`OPENING`) são ponteiros no snapshot e viram `NULL`.

**b) HTTP e SQS compartilham o mesmo service.**
O CLAUDE.md §4 diz que o handler do worker chama um service próprio, porque "regra diferente,
service diferente". Aqui a regra é **a mesma por exigência explícita** do SPEC §10: *"HTTP e
SQS devem compartilhar o caso de uso e as garantias de idempotência financeira."* Então:
**dois handlers, um service**. O princípio por trás da regra é respeitado — separa-se o que
muda por motivos diferentes, e aqui não muda.

**c) Entra `libs/cronjob`, o runtime periódico — irmão do `jobrunner`.**
Dois workers do SPEC não são consumidores de fila: o **publisher de outbox** (§11) e o
**resolvedor de referência pendente** (§7) são laços disparados por tempo sobre o Postgres.

O vocabulário fica explícito, porque os dois rodam no **mesmo processo** (`cmd/worker`):

| | disparado por mensagem | disparado por tempo |
|---|---|---|
| Runtime | `libs/jobrunner` | **`libs/cronjob`** |
| Porta | `jobrunner.Source` (`Consume`/`Ack`) | **`cronjob.Task`** (`Run`) |
| Arquivo no domínio | `handlers/<dominio>/job.go` | **`handlers/<dominio>/cronjob.go`** |
| Registro | `PrepareWorker(...)`, no domínio | **`Runner.Register(...)`, na `main` do worker** |
| Handler | `JobHandler` | **`CronjobHandler`** |

Assim **worker** é o processo, **job** é mensagem de fila e **cronjob** é tick periódico — três
palavras que hoje se confundiriam numa só.

O registro dos dois cai em lugares diferentes de propósito. `PrepareWorker` mora no domínio porque
**amarrar uma fila a um handler é decisão do domínio** — qual `Source`, qual handler. Um cronjob não
amarra nada: é só "rode isto de tempos em tempos". Então quem registra é a `main` do worker, com
`Runner.Register(handler)` — e **é essa chamada que prova, em tempo de compilação, que o handler
satisfaz `cronjob.Task`**. O ganho é o handler não importar o runtime: `CronjobHandler` é uma struct
com `Name`, `Interval` e `Run`, testável e legível sem o `libs/cronjob` junto.

Por que não um `group:"cronjobs"` do fx, como os checkers do `/health/ready`: porque o grupo é
montado por quem **provê**, e `cmd/server` monta os mesmos módulos de handler. As tarefas seriam
agendadas nos dois processos, e o publisher de outbox passaria a rodar no servidor HTTP. Quem
decide o que roda é o entrypoint, não o módulo.

O `cronjob` faz o que os dois laços precisam igual e que não é regra de negócio: ticker **com
jitter** (N réplicas não podem bater no banco no mesmo milissegundo), **tick imediato quando o
lote veio cheio** (esperar o intervalo com backlog é atraso de outbox de graça), span raiz e
error trail por tick, backoff em erro, e um `OnStop` que **espera o `Run` em andamento** — pelo
mesmo motivo do jobrunner: tem um `COMMIT` lá dentro. A porta é só isto:

```go
// Task is one unit of recurring work. Returning how many items it handled lets the runtime
// tick again immediately instead of idling while a backlog drains.
type Task interface {
    Name() string
    Interval() time.Duration
    Run(ctx context.Context) (handled int, err error)
}
```

Ele **não** abre transação (quem abre é o repositório, via `UnitOfWork`) e **não** faz eleição
de líder — múltiplos publishers é requisito do SPEC, e o `SKIP LOCKED` já é o mecanismo de
disputa.

**Por que não reusar o `jobrunner`.** O laço dele chama `Consume` e, quando não há mensagem,
faz `continue` **sem dormir**. Isso só é seguro porque o `ReceiveMessage` do SQS é long polling
e bloqueia até 20s com a fila vazia. Um `SELECT` no Postgres volta em 1ms: implementar `Source`
sobre o banco transformaria fila vazia em query em loop fechado. Somado a isso, `Consume`
devolve **uma** mensagem e o publisher quer lote, e `Ack` como `DeleteMessage` não descreve
"commitar o `UPDATE` que marca publicado".

**d) As filas passam a ser FIFO.**
A §17 já previu: *"A fila precisar de ordenação ou deduplicação → FIFO no SQS:
muda o adapter e a criação da fila. Nem o service nem o handler mudam."* É o caso. Muda
`init-queues.sh` e o adapter (`MessageGroupId`, `MessageDeduplicationId`).
### 4.2 Componentes

```
                        ┌─────────────────────────────────────┐
                        │            Keycloak (IdP)           │
                        │  realm · client_credentials · JWKS  │
                        └───────────────┬─────────────────────┘
                                        │ valida no boot (ja existe)
  Provider ──Bearer JWT──> ┌────────────┴───────────────┐
                           │   cmd/server (Echo)        │
                           │   middleware na rota       │
                           │   handlers/wallet   http   │
                           │   handlers/wagering http   │
                           └────────────┬───────────────┘
                                        │
  wager-transactions.fifo ──> ┌─────────┴────────┐       services/wagering
        (at-least-once)       │  cmd/worker      │       services/wallet
                              │  jobrunner       │──────> UMA transacao SQL
                              │  handlers/       │        (inbox + dominio +
                              │   wagering job   │         ledger + outbox)
                              │  cronjob         │              │
                              │   outbox         │              ▼
                              │   reference      │      ┌───────────────┐
                              └──────────────────┘      │  PostgreSQL   │
                                        │               └───────┬───────┘
                                        │ le outbox PENDING     │
                                        └───────────────────────┘
                                        │
                                        ▼
                              wager-events.fifo ──> consumidores externos
```
### 4.3 Estrutura de pastas do domínio

Segue o CLAUDE.md §3 sem exceção: um módulo por domínio em cada camada, contrato em
`interfaces/`, `fx` só em `module.go` e `cmd/`.

```
app/src/
├── interfaces/
│   ├── wallet/        service.go · repository.go · errors.go
│   ├── wagering/      service.go · repository.go · errors.go
│   ├── inbox/         repository.go
│   ├── outbox/        repository.go · publisher.go · errors.go
│   └── persistence/   unitofwork.go — nao e dominio: a porta da transacao,
│                      usada por todos os services
│
├── handlers/
│   ├── wallet/        http.go    POST /wallets, GETs, reconciliation
│   ├── wagering/      http.go    POST /wagering/transactions, GETs
│   │                  job.go     PrepareWorker — consumidor SQS
│   ├── outbox/        cronjob.go CronjobHandler — publisher da outbox
│   ├── reference/     cronjob.go CronjobHandler — pendencia de referencia
│   ├── health/        ja existe — ganha /health/live e /health/ready
│   └── identity/      ja existe
│
├── services/
│   ├── wallet/        opening.go · query.go · reconciliation.go
│   ├── wagering/      service.go     o caso de uso compartilhado HTTP+SQS
│   │                  reference.go   resolucao da pendencia
│   └── outbox/        publisher.go
│
├── repositories/
│   ├── wallet/        postgres.go
│   ├── wagering/      postgres.go
│   ├── inbox/         postgres.go
│   ├── outbox/        postgres.go
│   ├── persistence/   postgres.go — adapter generico da transacao
│   └── queue/         sqs.go — ja existe, ganha FIFO
│
├── entities/          wallet.go · wager_transaction.go · ledger_entry.go
│                      outbox_event.go · inbox_message.go
├── structs/           money.go · cursor.go · events.go · wager_message.go
│                      principal.go (ja existe)
└── libs/
    ├── cronjob/       NOVO — runtime periodico, irmao do jobrunner
    ├── db/            ja existe — ganha tx.go (Accessor + tx no contexto)
    └── ...            o resto ja existe
```
### 4.4 Onde cada regra mora

| Exigência | Camada |
|---|---|
| `Money`: parsing, aritmética, overflow, moeda | `entities/money.go` — value object do domínio, folha, com teste unitário (CLAUDE.md §13) |
| Invariante do agregado, transições, reidratação | `entities/` — folha, sem infra (ver §4.1a) |
| Caso de uso, ordem das operações, delimitação da transação | `services/` |
| Isolamento entre provedores em consulta e replay | `services/` — autorização que olha o dado é regra de negócio (§8) |
| Autenticação e papel de borda | `middleware`, declarado na rota |
| SQL, lock, claim de outbox | `repositories/` |
| Validação de borda, mapeamento para `apierr.Error` | `handlers/` |
### 4.5 A transação SQL

O SPEC §11 exige que estado, saldo, ledger, inbox e eventos sejam confirmados **atomicamente**.
O service precisa delimitar a transação **sem conhecer pgx** (o lint proíbe).

A porta **não pertence a domínio nenhum**: `services/wallet`, `services/wagering` e
`services/outbox` precisam dela, e a mesma transação atravessa os repositórios de todos eles.
Declará-la em `interfaces/wagering` faria `services/wallet` importar o domínio `wagering` só
para abrir uma transação. Ela segue então a regra que o CLAUDE.md §3 já tem para o que é da
camada mas não é domínio — a mesma de `repositories/queue/`:

```
interfaces/persistence/     unitofwork.go   a porta. Folha, sem driver, sem libs.
repositories/persistence/   postgres.go     o adapter, ao lado de queue/.
                                            A amarracao sobe para repositories/module.go.
libs/db/                    tx.go           o mecanismo: tx no contexto + Accessor.
```

> Nome do pacote: `persistence`, não `transaction`. Neste projeto "transaction" já é
> `wager_transactions` — um pacote com esse nome faria `transaction.UnitOfWork` conviver com
> `wagering.Transaction` significando coisas diferentes.

A porta, em `interfaces/persistence/unitofwork.go`:

```go
// UnitOfWork runs fn inside a single SQL transaction. Every repository called with the
// context fn receives joins that transaction; a non-nil return rolls it back.
type UnitOfWork interface {
    Atomic(ctx context.Context, fn func(ctx context.Context) error) error
}
```

O mecanismo que faz **qualquer** repositório entrar na transação sem saber quem a abriu mora em
`libs/db`, junto do pool:

```go
// Querier is what both a pool and a transaction satisfy.
type Querier interface {
    Query(context.Context, string, ...any) (pgx.Rows, error)
    QueryRow(context.Context, string, ...any) pgx.Row
    Exec(context.Context, string, ...any) (pgconn.CommandTag, error)
}

// Accessor hands a repository the querier for the context: the open transaction when there
// is one, the pool otherwise.
type Accessor struct{ pool *pgxpool.Pool }

func (a Accessor) Q(ctx context.Context) Querier
```

Cada `repositories/<dominio>/postgres.go` recebe o `Accessor` — **não** o `*pgxpool.Pool` — e
escreve `a.Q(ctx).Query(...)`. Essa é a parte que importa: como o pool não é alcançável, não
existe caminho em que um repositório escreva **fora** da transação por esquecimento. Sem isso,
a transação no contexto é implícita e silenciosa, que é o modo clássico de esse padrão falhar.

O adapter em `repositories/persistence/postgres.go` abre a `pgx.Tx`, guarda no contexto,
commita no retorno `nil` e faz rollback em erro ou pânico. `Atomic` aninhado **entra na
transação existente** em vez de abrir uma segunda: um service que compõe outro não vira duas
transações. **O service nunca vê `pgx`**, e a fronteira da transação fica legível no caso de
uso, que é onde ela é uma decisão.

**A transação é opcional, e a decisão é do service.** Fora de um `Atomic`, todo repositório
fala direto com o pool em auto-commit — é o caso de toda leitura. O service é quem sabe quais
escritas precisam cair juntas, e só essas ele embrulha:

| Operação | Abre transação? | Por quê |
|---|---|---|
| `POST /wallets` | **sim** | carteira + `OPENING` + ledger + 2 eventos num commit |
| `POST /wagering/transactions` | **sim** | transação + saldo + ledger + eventos |
| Consumo de uma mensagem SQS | **sim** | o mesmo, mais a inbox (SPEC §6.5) |
| Tick do resolvedor de referência | **sim**, uma por operação resolvida | cada resolução é um commit próprio |
| Tick do publisher de outbox | **não** — dois commits curtos | o `claim` e o `mark published` são transações separadas, e o `SendMessage` fica **fora** das duas: I/O de rede não segura transação aberta |
| `GET` de carteira, ledger, transação | **não** | leitura avulsa, auto-commit |
| `POST .../reconciliation` | **sim**, só leitura | precisa do snapshot consistente de §9 |

O que o service chama **não é um método de repositório**, e essa diferença é o ponto: com um
`repo.Begin()` o service ficaria segurando um handle de transação, passando-o adiante e
responsável por lembrar do commit e do rollback. Com `uow.Atomic(ctx, fn)` o escopo é léxico —
commit no retorno `nil`, rollback em erro ou pânico, e não existe forma de vazar uma transação
aberta. O service declara **o que é atômico**; quem abre, commita e desfaz é o adapter.

O caminho completo, dentro de um `Atomic`:O caminho completo, dentro de um `Atomic`:

```
BEGIN
  1. INSERT inbox_messages            (so no caminho SQS — ON CONFLICT DO NOTHING)
  2. SELECT ... FROM wallets WHERE id = $1 FOR UPDATE
  3. regra de dominio, em Go, sobre o agregado reidratado
  4. INSERT wager_transactions        (status final, failure_code, result_balance_minor)
  5. UPDATE wallets SET balance_minor, version = version + 1
  6. INSERT wallet_ledger_entries     (exceto LOSS e rejeicao)
  7. INSERT outbox_events             (snapshot imutavel, com o saldo ja consolidado)
  8. UPDATE inbox_messages SET completed_at = now()
COMMIT
```

A ordem importa em dois pontos. O `FOR UPDATE` vem **antes** da regra, porque a regra decide
sobre um saldo que precisa continuar valendo no commit. E a outbox vem **depois** do domínio,
porque o payload carrega `balanceAfter` e `walletVersion` — valores que só existem no passo 5.
### 4.6 Outbox

**Escrita** — na transação do domínio, sempre. O repositório recusa o `Insert` fora de um
`Atomic` (`persistence.ErrNoTransaction`): um evento gravado sozinho poderia sobreviver à mudança
que descreve, que é exatamente a publicação antes do commit que a outbox existe para impedir.

**Publicação** — um cronjob (`libs/cronjob`, ver §4.1) em toda instância do
worker. Cada tick chama `PublishDue`, que faz três coisas em três passos curtos, **nenhum deles
segurando transação durante a chamada de rede**:

```
1. CLAIM     UPDATE ... SET locked_by, locked_at, attempts = attempts + 1
             WHERE event_id IN ( SELECT ... FOR UPDATE OF o SKIP LOCKED LIMIT n )   -- commit próprio
2. PUBLISH   SendMessage na fila FIFO de eventos                                     -- fora de transação
3. RECORD    UPDATE ... SET status = 'PUBLISHED'   (ou, se falhou: next_attempt_at em backoff)
```

O que faz isso correto com **N publishers e nenhum coordenador**:

- **`SKIP LOCKED`**: cada publisher leva um lote diferente, ninguém espera ninguém.
- **Lease**: um evento reservado só volta a ser elegível quando `locked_at` ficou mais velho que
  `OUTBOX_LEASE`. É a recuperação do trabalho abandonado: o publisher que morreu segurando um lote
  perde os eventos para o próximo tick de qualquer instância.
- **Ordem por agregado**: um evento **não é elegível enquanto houver um anterior do mesmo
  agregado ainda `PENDING`** (esperando retry ou reservado por outro publisher). Sem isso, dois
  publishers poderiam pegar dois eventos da mesma carteira e enviar o mais novo primeiro. O preço,
  declarado: um evento que falha segura os que vêm atrás **só do próprio agregado**, pelo tempo do
  seu backoff.
- **Backoff**: `OUTBOX_BACKOFF_BASE` dobrado a cada tentativa até `OUTBOX_BACKOFF_MAX`, com
  espalhamento de ±20% para instâncias que falharam juntas não tentarem juntas. **Nunca desiste**:
  o evento fica `PENDING` com espera limitada, porque perder um evento cujo registro foi
  confirmado é exatamente o que o SPEC proíbe.
- **Republicação**: se o publisher morre entre o broker aceitar e a linha ser marcada, a linha
  continua `PENDING`, o lease expira e outro publisher envia **o mesmo `eventId`**. A fila FIFO
  descarta a segunda cópia dentro da janela de deduplicação (o `MessageDeduplicationId` é o
  `eventId`), e o consumidor deduplica por ele além da janela. É at-least-once assumido, não
  escondido.
- **`Complete` idempotente**: `WHERE status = 'PENDING'`. Dois publishers que receberam o mesmo
  evento após um lease expirar não brigam: o segundo encontra a linha pronta e isso não é erro.
- **`Release` só se ainda for meu** (`locked_by = $publisher`): quem perdeu o lease não
  sobrescreve o que o outro decidiu.

Configuração (`.env.example`): `OUTBOX_POLL_INTERVAL`, `OUTBOX_BATCH_SIZE`, `OUTBOX_LEASE`,
`OUTBOX_BACKOFF_BASE`, `OUTBOX_BACKOFF_MAX`. O lease precisa exceder o tempo de publicar um lote.

**Contrato de saída.** Destino: fila FIFO `wager-events.fifo`, com DLQ `wager-events-dlq.fifo`
(`maxReceiveCount` 5), provisionadas por `docker/localstack/init-queues.sh` e por
`infra/modules/queue`.

| Campo do SQS | Valor | Para quê |
|---|---|---|
| `MessageGroupId` | `aggregateId` | os eventos de um agregado chegam na ordem em que foram enviados |
| `MessageDeduplicationId` | `eventId` | uma republicação é reconhecida e descartada pela fila |
| `MessageBody` | o envelope (§2.8), **exatamente como gravado** | snapshot imutável |
| atributos | `eventType`, `eventId`, `correlationId`, `otel-*` | roteamento e rastreio sem parsear o corpo |

**Limitação declarada.** A ordem por agregado vale por `aggregateId`. A carteira e a transação
são agregados diferentes, então a ordem **entre** um `WagerTransactionProcessed` e o
`WalletBalanceChanged` da mesma operação não é garantida; o consumidor que precisa ordenar
mudanças de saldo usa `walletVersion`, que existe para isso.

### 4.7 Filas

| Fila | Papel | `MessageGroupId` | `MessageDeduplicationId` |
|---|---|---|---|
| `wager-transactions.fifo` | entrada: operações dos provedores | `walletId` | `idempotencyKey` |
| `wager-transactions-dlq.fifo` | redrive (`maxReceiveCount` 5) e o que o consumidor desiste | — | hash do corpo |
| `wager-events.fifo` | saída da outbox (§4.6) | `aggregateId` | `eventId` |
| `wager-events-dlq.fifo` | redrive da saída | — | — |

As filas são criadas pela infraestrutura (`docker/localstack/init-queues.sh` e
`infra/modules/queue`), nunca pela aplicação.

**Por que o grupo é a carteira.** O SQS FIFO entrega um grupo de cada vez e em ordem: as operações
de uma carteira são consumidas uma por vez, na ordem em que foram enviadas, enquanto carteiras
diferentes andam em paralelo. É o mesmo particionamento do lock de linha (§2.7), então a fila e o
banco concordam sobre o que precisa ser serial.

**A deduplicação da fila não é a garantia.** O `MessageDeduplicationId` só vale por 5 minutos e o
produtor pode escolher outro. O que garante que uma operação se aplica uma vez são a inbox e os
índices únicos (§2.6), e é por isso que os testes reenviam o mesmo corpo com **outro**
deduplication id: a fila esconderia exatamente o que se quer provar.

**O contrato da mensagem** (`data` tem os campos do `POST /wagering/transactions` mais
`idempotencyKey`, porque uma mensagem não tem header):

```json
{ "messageId": "msg-123", "type": "WagerTransactionRequested", "occurredAt": "2026-09-08T12:00:00.000Z",
  "data": { "providerId": "provider-a", "externalTransactionId": "transaction-123",
            "idempotencyKey": "provider-a:transaction-123", "playerId": "…", "walletId": "…",
            "roundId": "round-987", "gameId": "fortune-chimp", "kind": "BET",
            "money": { "amount": "25.00", "currency": "BRL" } } }
```

**A inbox e a transação única.** O registro da inbox, tudo o que a operação faz (transação, saldo,
ledger, eventos) e a conclusão do tratamento são **um commit**. É o que torna seguro apagar a
mensagem depois, e inofensivo um crash entre o commit e a remoção: a mensagem volta, a inbox já a
tem, nada se repete. O `Insert` usa `ON CONFLICT DO NOTHING` para uma duplicata não abortar a
transação, e a chave `(consumer, messageId)` faz duas entregas simultâneas se serializarem: a
segunda espera a primeira e encontra o registro. O hash do **corpo exato** distingue uma reentrega
de outra mensagem que reutilizou o id.

**Três destinos para uma mensagem**, decididos no handler a partir do que o service devolveu:

| O que aconteceu | Destino |
|---|---|
| aplicada, replay ou **rejeição de negócio** (gravada como `REJECTED`) | apagada da fila |
| nenhuma retentativa muda o resultado: corpo malformado, tipo desconhecido, `OPENING`, conflito de idempotência, conflito de `messageId` | **enviada à DLQ com o motivo** (`failureReason`) e só então apagada |
| armazenamento indisponível (transitório) | **não apagada**: volta após o visibility timeout, e depois de `maxReceiveCount` o SQS a manda à DLQ |

Se a própria DLQ não puder ser alcançada, o erro é devolvido e a mensagem não se perde: volta e é
descartada de novo. `WALLET_NOT_FOUND` é o único caso de rejeição sem registro (não há carteira para
prender a transação): a resposta é definitiva, a mensagem é apagada e não há linha na inbox.

**Limites.** `maxReceiveCount` = 5, `VisibilityTimeout` = 60 s (deve exceder o handler mais lento).
Uma mensagem que falha sempre chega à DLQ depois de 5 recebimentos.

**`SIGTERM`.** O `jobrunner` para de buscar e o `OnStop` espera as mensagens em andamento; o
contexto de cada mensagem não herda o cancelamento do laço, então um commit em curso termina. O que
não terminar no prazo fica sem ack e volta pela fila, sem efeito duplicado por causa da inbox.

### 4.8 Contratos HTTP

| Rota | Autorização | Códigos |
|---|---|---|
| `POST /wallets` | `internal_service` | `201` · `409` já existe · `400` |
| `GET /wallets/:id` | `internal_service` | `200` · `404` · `400` |
| `GET /wallets/:id/ledger` | `internal_service` | `200` (cursor opaco) · `400` cursor ou limit inválido · `404` |
| `POST /wallets/:id/reconciliation` | `internal_service` | `200` · `404` |
| `POST /wagering/transactions` | `provider`, e o `providerId` do corpo é o do token | ver abaixo |
| `GET /wagering/transactions/:id` | `provider`; só a **própria** transação | `200` · `404` (inclusive para a de outro provedor) |
| `GET /providers/:providerId/wagering/transactions/:externalId` | `provider`; `:providerId` é o do token | `200` · `403` (outro provedor) · `404` |
| `GET /health/live`, `/health/ready` | pública | `200` · `503` |

Operações de carteira são do serviço interno (SPEC §2); um provedor não lê carteira, ledger nem
reconciliação, e o serviço interno não lê transação de provedor. Cada rota declara o papel na
própria definição.

`POST /wagering/transactions`:

| Código | Quando | Corpo |
|---|---|---|
| `200` | processada, ou replay idempotente | `TransactionResponse`: `transactionId` · `status` · `balance` · `idempotentReplay` |
| `202` | `PENDING_REFERENCE` | `TransactionResponse`: `transactionId` · `status` |
| `400` | payload inválido, `Idempotency-Key` ausente, `OPENING` recebido | `APIError` (com `failureCode` quando há um) |
| `401` / `403` | token ausente/inválido · `providerId` divergente do token | `APIError` |
| `409` | chave reusada com outro conteúdo, ou operação já existente com outra chave | `APIError` |
| `422` | rejeição de negócio: **gravada e reproduzível**, corpo `TransactionResponse` com `status: REJECTED`, `failureCode` e `transactionId`. Só `WALLET_NOT_FOUND` responde `422` sem `transactionId`, porque não há o que gravar | `TransactionResponse` / `APIError` |
| `503` | Postgres ou SQS indisponível, verificador não carregado | `APIError` |

**`422` × `409` × `503` é a distinção que o SPEC §9 cobra.** `409` é "sua chave está errada" —
corrija o cliente. `422` é "sua operação foi recusada" — a regra decidiu, o resultado é
definitivo e auditável. `503` é "tente de novo" — nada foi decidido. Um cliente que trate os
três igual vai reenviar o que não deve ou desistir do que daria certo.

**Isolamento entre provedores.** Na escrita, o `providerId` do corpo tem de ser o do token (`403`
senão), decidido antes de ler ou gravar qualquer coisa. Na **leitura** não há corpo, então quem
filtra é o service, sempre pelo provedor da **identidade**, nunca pelo da URL. Uma transação de
outro provedor responde `404`, **exatamente como uma que não existe**: existe um teste unitário que
compara as duas mensagens, porque a diferença entre elas diria quais ids pertencem a alguém. Já o
caminho `/providers/:providerId/…` com o provedor errado é `403`: é decidido só pela comparação com
o token, sem tocar em dado nenhum, então não revela existência.

**Paginação do ledger.** O cursor é opaco (`base64url("v1.<seq>")`) e a ordem é o `seq` do
`IDENTITY` da coluna, que nunca muda nem se repete. Como toda escrita numa carteira é serializada
pelo lock da linha, o `seq` de uma carteira cresce na ordem do commit: um lançamento gravado
enquanto o cliente lê aparece numa página posterior, e nenhum é pulado nem visto duas vezes. O teto
de página (200) é regra do service; sem `limit` vale 50, e um `limit` presente que não seja um
inteiro ≥ 1 é `400` — dizer `limit=0` não é pedir o padrão.

**Reconciliação.** `POST /wallets/:id/reconciliation` lê o saldo gravado e a soma do ledger
(`créditos − débitos`, abertura incluída) **numa única transação `REPEATABLE READ` somente leitura**
(`UnitOfWork.Snapshot`). Lidos um depois do outro, um movimento que commitasse entre as duas leituras
faria uma carteira saudável parecer divergente, e o relatório seria tão errado quanto o que ele
verifica. `difference = saldo gravado − saldo reconstruído`. Uma divergência vai na resposta e num
log de erro, e **nunca é corrigida**: reescrever o saldo destruiria a evidência.

---

## 5. O quadro das decisões

| Decisão | Ferramenta | Em vez de |
| Injeção de dependência e ciclo de vida | **Uber `fx`** | fiação manual no `main` |
| Identidade | **Keycloak (OIDC)**, chaves carregadas no boot | `session/` com HMAC + bcrypt |
| Segundo processo | **worker com handler e service próprios, mesmo source code** | não existia |
| Fila | **SQS** (LocalStack em dev), com ack explícito | não existia |
| Observabilidade | **OpenTelemetry**, exportando OTLP direto | não existia |
| Log | **`zap`**, atrás do `Observer` | `zerolog` |
| Banco | **pgx** + SQL escrito à mão | GORM |
| Erros | stdlib `errors` + `%w` + `errors.Is/As` | `github.com/pkg/errors` |
| Migração | **goose CLI** | goose CLI (igual) |
| Teste | unitário em `services`/`entities`/`structs` + e2e com testcontainers | unitário em `services` |

---

## 6. Uber `fx`: injeção de dependência e ciclo de vida

Todo componente é um construtor que **declara o que precisa nos parâmetros**. O `fx` resolve o
grafo por tipo. Cada pacote publica um `Module`, e cada entrypoint escolhe **quais módulos
monta**.

```go
// app/cmd/server/main.go — a diferença inteira entre os dois binários
fx.Supply(appinfo.App{Name: "pedro-test-server", Role: appinfo.RoleServer}),
bootstrap.Core,       // config, observabilidade, db, awsclients, repositories, services
auth.ServerModule,    // valida token de pessoa
handlers.Module,      // os handlers, registrados logo abaixo por ServerRoutes
```

### Por que

A regra geral é **não** criar injeção de dependência no dia 1: ela se paga quando a fiação
vira bagunça no `main`. Este projeto já nasce nesse sintoma, por um motivo estrutural:
existem **dois processos** que compartilham quase todo o grafo e divergem só na ponta. Sem DI
as opções seriam duas `main` duplicando a construção de config, logger, tracer,
pools, repositórios e services — que divergem no primeiro dia em que alguém mexer numa só —,
ou uma fábrica com um booleano `isWorker` dentro.

Três coisas que a fiação manual resolve mal e o `fx` resolve:

- **Ciclo de vida com ordem.** Pool antes do servidor, servidor derrubado antes do pool. O
  `OnStop` do worker **espera** os jobs em andamento; o do provider de trace **descarrega** os
  spans ainda em buffer — justamente os da requisição que derrubou o processo.
- **Falha de boot que desfaz o que já subiu.** Com `defer` no `main`, a metade que subiu antes
  do erro vaza.
- **Erro de fiação vira teste.** `fx.ValidateApp` valida o grafo **sem executar construtor
  nenhum** — não abre banco nem toca a fila. É o `main_test.go` de cada entrypoint.

### O custo, declarado

DI troca um erro de compilação por um erro de execução: porta sem adapter só aparece no boot.
Por isso o `main_test.go` existe. E **`fx` só é permitido em `module.go` e em `cmd/`** — o
lint proíbe importá-lo dentro das camadas, porque fiação não é regra de negócio.

---

## 7. Um source code, dois entrypoints

Este é o ponto que mais mudou de forma, e ele copia o **monolito do coruja**: lá, cada app de
domínio expõe `ServerRoutes(...)` e `PrepareWorker(...)`, e `cmd/server/main.go` e
`cmd/worker/main.go` chamam o que cada processo precisa. Aqui é a mesma coisa:

```go
// handlers/<dominio>/http.go
func ServerRoutes(g *echo.Group, h *HTTPHandler)

// handlers/<dominio>/job.go
func PrepareWorker(runner *jobrunner.Runner, source jobrunner.Source, h *JobHandler)
```

```
                    ┌──────────── libs/bootstrap.Core ─────────────┐
                    │ config · observability · db · awsclients ·   │
                    │ repositories · services                      │
                    └──────────────────────────────────────────────┘
                         ▲                                    ▲
          cmd/server ────┘                                    └──── cmd/worker
   Echo + middlewares + ServerRoutes por domínio      jobrunner + PrepareWorker por domínio
   auth.ServerModule (valida JWT de pessoa)           auth.WorkerModule (service account)
                                                      + probe HTTP próprio
```

### Por que o servidor mora na `main`

A versão anterior deste projeto tinha um `libs/httpserver` que construía o Echo, registrava as
rotas e cuidava do ciclo de vida. Ele saiu, e o conteúdo foi para `cmd/server/main.go`.

O motivo é o mesmo pelo qual o monolito faz assim: **o mapa do que um processo serve tem que
estar no arquivo que define o processo.** Com um pacote de runtime no meio, a resposta para "o
que esta aplicação expõe?" exige abrir um pacote que não é nem camada nem domínio, e que
inevitavelmente vira o lugar onde todo mundo mexe — um arquivo que cresce com cada domínio e
que todo PR toca. Com o registro na `main`, acrescentar um domínio é **uma linha na `main` e um
arquivo no domínio**, e as rotas de um domínio moram com ele.

O mesmo vale para o worker: `cmd/worker/main.go` monta o `jobrunner` e chama os
`PrepareWorker`. O que sobrou em `libs/jobrunner` é só o **runtime** — laço de polling,
concorrência, ack e desligamento gracioso —, que não conhece domínio nenhum e é o equivalente
exato do `sqsworker` da backend-libs no monolito.

### A documentação da API: swag + Swagger UI

As rotas continuam sendo Echo puro. A documentação vem de **[swag](https://github.com/swaggo/swag)**,
que lê anotações ao lado de cada handler e gera o documento Swagger em `app/docs`, servido pelo
**Swagger UI**:

```go
//	@Summary	Cria um pedido
//	@Tags		pedidos
//	@Param		request	body		CreateRequest	true	"Pedido a criar"
//	@Success	201		{object}	Response
//	@Failure	401		{object}	structs.APIError
//	@Security	OAuth2Password
//	@Router		/pedidos [post]
func (h *HTTPHandler) Create(c echo.Context) error
```

> Os exemplos com `pedido` ao longo deste documento são ilustrativos: o repositório não tem
> domínio nenhum hoje (ver §1). Eles mostram a forma que o primeiro vai ter.

**Por que anotação e não um framework que deriva do tipo.** A alternativa avaliada foi o huma,
que gera o OpenAPI a partir dos tipos de entrada e saída e elimina por construção o estado "a
doc está desatualizada". Ela foi implementada e desfeita, por duas razões: o handler deixava de
ser Echo e passava a ser um tipo do framework — mais uma camada no caminho de toda requisição,
num projeto cujo desenho inteiro é sobre manter o caminho explícito —, e o adapter de Echo do
huma pulou para `echo/v5` na v2.38, o que amarraria a doc da API a uma migração de framework
web.

Com swag, o registro de rota continua sendo `e.GET("/pedidos", h.List, requireAuthentication)`, e
a doc é metadado ao lado do handler.

**O que se paga, declarado:** a anotação pode divergir do código, porque nada obriga as duas a
concordarem. É o oposto do trade-off do huma, e por isso a mitigação é um cenário de ponta a
ponta que exige que **toda rota registrada apareça no documento** — a falha real desse modelo é
rota nova sem anotação, servida e invisível.

Isso também abre a única exceção à regra de comentário do projeto: anotação de swag é metadado
estruturado, não prosa, e por isso convive com "só GoDoc e fluxo fora do padrão".

**Três detalhes que não são óbvios:**

- **`Response` não é a entidade.** O handler responde uma struct própria, mapeada da entity. O payload que a API promete e a linha que a tabela guarda mudam por motivos
  diferentes, e uma coluna renomeada não deveria quebrar todo cliente. De quebra, é o que a doc
  descreve.
- **O modelo de erro é um tipo nomeado** (`structs.APIError`), e é ele que o `@Failure`
  referencia. O Echo renderiza um `HTTPError` exatamente nessa forma, então o que a rota
  devolve e o que a doc descreve são a mesma coisa — "o que o framework renderizar" não é uma
  descrição.
- **A `tokenUrl` do documento é reescrita em tempo de execução.** O swag a grava na geração, e o
  endereço do realm muda por ambiente: a aplicação fala com o Keycloak pelo endereço interno e o
  navegador pelo externo. `docsRoutes` serve o documento com a URL de `Keycloak.PublicTokenURL()`,
  e há um cenário de e2e afirmando isso.

### A rota de documentação é opcional, e desligada por padrão

`DOCS_ENABLED` decide se `/docs`, `/swagger/*` e o documento existem. Desligada, **as rotas não
são registradas** — não há o que proibir, e a resposta é 404 em vez de 403.

A escolha é deliberada: o controle mais confiável para uma superfície opcional é ela não
existir. Em sandbox a flag fica ligada, que é onde a página serve para alguma coisa. Se um dia
for preciso expor a doc em produção, a proteção vem da borda — ver o TO DO na §21.

### O worker não é um lugar onde mora lógica

Ele tem a mesma anatomia do servidor:

| | Server | Worker |
|---|---|---|
| Runtime (transporte) | Echo, em `cmd/server/main.go` | `libs/jobrunner` |
| Entrega | `handlers/<dom>.HTTPHandler` | `handlers/<dom>.JobHandler` |
| Regra | `services/<dom>.Service` | `services/<dom>.CompletionService` |
| Dado | `repositories/<dom>.PostgresRepository` | o mesmo |
| Identidade | valida o JWT de quem chamou | service account (`client_credentials`) |

Consumir fila **é entrega**, então tem handler — e ele mora no **mesmo módulo de domínio** que
o handler HTTP, porque é o mesmo domínio entrando por outra porta. E o handler do worker chama
um **service próprio**: criar um pedido e concluir um pedido são decisões diferentes, com regras
diferentes (a segunda precisa ser idempotente, a primeira não). Compartilhar um service só
porque as duas mexem na mesma tabela seria juntar o que muda por motivos diferentes.

O que é compartilhado de verdade é tudo o que está abaixo: config, telemetria, pools, clients,
repositórios. Uma correção ali vale para os dois processos no mesmo commit.

### Decisões do worker que não são óbvias

- **O worker serve um `/health` próprio.** Um orquestrador precisa saber se o consumidor está
  vivo, e um processo sem superfície HTTP não tem como dizer. É o mesmo que o
  `cmd/worker/main.go` do monolito faz. O probe não passa pelo middleware de telemetria: uma
  requisição a cada poucos segundos seria a maior parte do volume de trace do worker e nada do
  seu significado.
- **O contexto da mensagem não herda o cancelamento do laço.** O desligamento não pode matar
  uma mensagem no meio; quem espera por ela é o `OnStop`.
- **Falha ao publicar um job não deve derrubar a escrita que já aconteceu.** Se a linha foi
  gravada, devolver erro faz o cliente reenviar e duplicar. O job perdido é recuperável por uma
  varredura do que ficou pendente; a duplicata não é. A falha **é registrada** — §11.

## 8. Keycloak como IDP

A API não guarda senha, não emite sessão e não tem tabela de usuário. Ela **valida** o Bearer
JWT contra as chaves do realm e transforma as claims num `structs.Principal`. O worker, que
não tem ninguém na frente, se autentica como **service account** (`client_credentials`) no
mesmo realm.

### Por que

Autenticação é um problema resolvido e caro de errar. Guardar senha com bcrypt e emitir um
cookie assinado serve a um projeto que nasce sozinho; aqui há dois processos e a perspectiva
de mais serviços, e centralizar identidade evita que cada um tenha a sua noção de quem é quem. Máquina e pessoa vêm do mesmo realm, e trocar o papel de alguém é mexer na
configuração do IDP, não fazer deploy.

### Decisões que não são óbvias

- **As chaves são carregadas no START da aplicação**, num `fx.Lifecycle.OnStart` com retry
  (10 tentativas, 2s). Issuer errado, realm inexistente ou segredo trocado derrubam o boot —
  na tela de quem fez o deploy, não na primeira requisição autenticada de madrugada. O preço
  é que a API passa a depender do Keycloak para subir, e o compose reflete isso
  (`depends_on: keycloak: service_healthy`).
- **Enquanto não carregou, a resposta é 503 e não 401.** Verificador sem metadado é problema
  do servidor; devolver 401 mandaria o cliente achar que o token dele é inválido.
- **`Issuer` e `KEYCLOAK_INTERNAL_URL` são campos separados.** O `iss` do token é o endereço
  externo (`localhost:8080`); o metadado é buscado pelo endereço interno (`keycloak:8080`).
  `oidc.InsecureIssuerURLContext` é o mecanismo do go-oidc para exatamente isso: buscar de um
  endereço e exigir que o documento declare o outro. Confundir os dois é a causa número um de
  "issuer mismatch" em dev.
- **Algoritmo fixado em RS256.** Fecha a porta do `alg: none` e da troca de algoritmo.
- **`/health` fica fora da autenticação.** Probe que depende do IDP transforma uma queda do
  Keycloak em reciclagem de todas as tasks do ECS.
- **Autorização de borda no handler; autorização que olha o dado no service.**
  `RequireRealmRole("vendas")` é borda. "Só o dono pode fechar o pedido" seria regra de negócio.

### O modelo de autorização do domínio

A identidade autenticada decide **quem o chamador é**; a rota decide **o que ele pode tocar**.
São dois papéis de realm, e nenhuma pessoa nem serviço tem os dois:

| Identidade (realm `pedro-test`) | Papel | `provider_id` no token | Pode |
|---|---|---|---|
| `pedro-test-wallet-service` | `internal_service` | — | abrir carteira, ler carteira e ledger, reconciliar |
| `provider-a`, `provider-b` | `provider` | claim fixa `provider-a` / `provider-b` | enviar operações e ler **as próprias** transações |
| `pedro-test-short-lived` | `internal_service` | — | só existe nos testes: token de 1 segundo, para provar a rejeição de um token **assinado e expirado** |
| `pedro-test-worker` | — | — | consumir a fila (a autorização do broker é da fila, ver §9) |

Todas são contas de máquina, autenticadas por `client_credentials`. A escolha do Keycloak, a
validação offline por JWKS e RS256 fixado estão acima; o que este domínio acrescenta é:

- **O provedor autorizado vem do token, nunca do corpo.** O `provider_id` é uma claim
  fixada pelo realm no cliente do provedor (`oidc-hardcoded-claim-mapper`), lida para
  `structs.Principal.ProviderID`. Um provedor não escolhe quem é.
- **A rota declara o papel** (`RequireRealmRole("internal_service")`), como qualquer autorização
  de borda. A regra que precisa olhar o dado — "este provedor só lê as próprias transações" — é
  do service, porque depende do registro e não do token.
- **Sem token, token adulterado, token expirado ou papel errado: nenhum efeito.** Os cenários
  conferem o banco depois (carteira, transação, ledger e evento), não só o status da resposta.

---

## 9. Fila: SQS com ack explícito

A fila é **SQS** — em dev, LocalStack. Não há cache no projeto: o Redis foi retirado por ora, e
o que ele poderia aliviar está registrado no [TO DO da §22](#22-to-do--cache-de-leitura-e-cdn).

- **`libs/awsclients` só constrói o client**; quem envia e recebe é `repositories/queue`, que
  implementa `jobrunner.Source` (consumir) e expõe `Publish` (produzir). É a mesma regra do
  pool do Postgres. O adapter carrega **bytes**: o que a mensagem significa é do domínio, e um
  adapter que a desserializasse teria de conhecer todo tipo de mensagem que vier a existir.
- **O trace viaja em message attributes** (prefixo `otel-`), não dentro do corpo. O corpo é o
  payload do domínio: um consumidor escrito por outra pessoa tem que conseguir ler o job sem
  saber que esta aplicação embrulha alguma coisa nele.
- **O contrato do handler tem duas saídas, e só duas.** `nil` → o `jobrunner` chama `Ack`, que
  é um `DeleteMessage`: a mensagem sai da fila. Erro → **não** apaga: a mensagem volta a ficar
  visível quando o *visibility timeout* expira e é entregue de novo, até o `maxReceiveCount`
  mandá-la para a DLQ. Não há como pedir uma terceira coisa, e isso é deliberado: "descartar
  sem processar" e "concluir sem fazer nada" são a mesma resposta (`nil`), porque do ponto de
  vista da fila são.
- **Consequência direta: todo handler de job é idempotente.** Entrega é *at least once*, e a
  mesma mensagem vai chegar duas vezes mais cedo ou mais tarde. A outra consequência é que
  **falha que retry nenhum conserta devolve `nil`**: mensagem apontando para um registro que
  não existe mais está concluída, não falhada, e tratá-la como erro encheria a DLQ de coisas
  que ninguém pode consertar. Três cenários de ponta a ponta cobrem o contrato — apagou,
  voltou, e voltou e depois apagou.
- **Ack que falha é ruído com dono.** O trabalho foi feito mas a mensagem não saiu da fila, e
  ela vai chegar de novo. Por isso a falha é logada: uma fila que não drena é indistinguível,
  de fora, de um worker parado.
- **A fila e a DLQ são criadas pela infraestrutura**, nunca pela aplicação — em dev, o
  `docker/localstack/init-queues.sh`. Em AWS a aplicação não teria permissão para criar a
  própria fila, e é melhor que o dev local tenha a mesma restrição.

---

## 10. Postgres com pgx

`pgxpool` + SQL escrito à mão, sem ORM.

- pgx é o driver nativo do protocolo do Postgres — sem a camada `database/sql` no meio —, e é
  o que dá acesso aos tipos do Postgres sem tradução.
- SQL à mão é **legível no log e no `EXPLAIN`**: a query que aparece no plano é a mesma que
  está no arquivo. Com ORM, revisar o índice de uma listagem exige primeiro reconstruir
  mentalmente o SQL que a biblioteca vai gerar.
- O scan continua barato: `pgx.CollectRows` + `pgx.RowToStructByName` preenche a entidade
  pelas tags `db:"..."` — que são string, e por isso podem ficar em `entities/` sem arrastar o
  driver para o domínio.
- `pgx.ErrNoRows` vira a sentinela do contrato (`<dominio>.ErrNotFound`) dentro do
  repositório, então quem chama nunca precisa conhecer pgx.

---

## 11. Observabilidade: OpenTelemetry + `zap`

### Por que OpenTelemetry, e não o SDK de um fornecedor

Porque o que sai da aplicação deixa de ser uma decisão da aplicação. OTLP é um protocolo
aberto: o mesmo `span.RecordError`, o mesmo histograma e o mesmo `trace_id` podem ir para
Grafana/Tempo, New Relic, Datadog, Honeycomb, AWS X-Ray ou um coletor que faça *fan-out* para
vários ao mesmo tempo — **trocando um endereço, não o código instrumentado**.

Isso importa por três motivos concretos:

- **Migrar de fornecedor não é reescrever a instrumentação.** Com um agente proprietário, a
  telemetria fica amarrada à biblioteca dele: sair custa tocar em todo arquivo que mede alguma
  coisa. Aqui custa `OTEL_EXPORTER_OTLP_ENDPOINT`.
- **Dá para mandar para mais de um lugar.** Manter o fornecedor atual e provar o novo em
  paralelo é configuração do coletor; a aplicação nem fica sabendo.
- **O vocabulário é o mesmo em qualquer backend.** `service.name`, `trace_id`,
  `app.layer` — as convenções são do padrão, então um trace continua legível mesmo quando a
  tela onde ele aparece muda.

Não é hipótese distante: este projeto já rodou a mesma instrumentação contra um coletor OTLP e,
depois, direto contra o Grafana LGTM. A diferença entre as duas configurações é uma variável de
ambiente.

### Uma superfície só: `observability.Observer`

Trace, métrica e log são a **mesma decisão tomada três vezes sobre o mesmo evento** ("esta
operação começou / demorou tanto / falhou assim"). Três dependências fariam cada camada
receber três parâmetros e — pior — permitiriam abrir o span e esquecer de logar o erro. Então
existe um tipo só, injetado em toda camada:

```go
func (s *service) List(ctx context.Context) ([]entities.Pedido, error) {
    return observability.Trace(ctx, s.obs, observability.LayerService, "pedido.Service.List",
        func(ctx context.Context) ([]entities.Pedido, error) {
            return s.pedidos.List(ctx)
        })
}
```

O span é encerrado **com o erro que a função devolveu**. Não-nulo vira `span.RecordError` +
status de erro + contador de falha + log em nível `error`. **Não há caminho em que um erro suba
sem ser registrado.** Um panic encerra o span como falha e segue para o middleware de recover.

> **Sem retorno nomeado.** A versão anterior abria o span com `defer func() { end(err) }()`, e
> isso obrigava a função a declarar `(x T, err error)`: só um `err` nomeado é visto pelo `defer`
> depois do `return`, e a forma curta (`defer end(err)`) avalia `err` ainda `nil`. Retorno nomeado
> convida a `return` sem operandos, a atribuir a variável errada dentro de um closure aninhado e a
> devolver, sem querer, um valor pela metade junto com o erro. `Trace` e `TraceErr` tiram a
> necessidade: o helper vê o retorno do closure, e a função devolve variáveis locais e declara só
> os tipos. Onde é preciso agir em volta do span — registrar o resultado, ou encerrar com um erro
> diferente do devolvido, como a leitura que não acha nada —, usa-se `Start` com o corpo numa
> função interna, e o `end(...)` é chamado antes do `return`.

### Todas as camadas

Cada span carrega `app.layer` (`handler`, `service`, `repository`, `gateway`). Sem esse
atributo, um trace lento diz "levou 900ms" e cala sobre onde:

```
GET /pedidos                        [otelecho — span do servidor]
└─ GET /pedidos                     app.layer=handler
   └─ pedido.Service.List           app.layer=service
      └─ pedido.Postgres.List       app.layer=repository
```

E atravessando a fila:

```
POST /pedidos (pedro-test-server)     └─ pedido.JobHandler.Handle (pedro-test-worker)
└─ pedido.Service.Create                 └─ pedido.CompletionService.Complete
   └─ queue.Publish ──────────────────────┘  (mesmo trace_id, via message attributes)
```

### Segregação por aplicação

`appinfo.App` é o único valor que difere entre os entrypoints além dos módulos. Dele saem o
`service.name` (`pedro-test-server` / `pedro-test-worker`) e o campo `app` de **toda** linha de
log, inclusive as que não passam pelo `Observer` (as do `fx` e as do Echo). Nenhum dos dois é
configuração de ambiente: são o que o binário **é**, e por isso não têm como divergir de um
`.env` esquecido.

### Exportação direta, sem coletor

A aplicação fala OTLP (gRPC) **direto** com o backend de observabilidade. O compose sobe um
`grafana/otel-lgtm`: um contêiner com ingestão OTLP, Prometheus, Tempo, Loki e o Grafana já
provisionado.

O coletor existe para resolver problemas que este projeto não tem: fan-out para vários
destinos, enriquecimento de atributo fora do código, buffer entre a aplicação e o backend.
Enquanto eles não existem, ele é um serviço a mais para subir, configurar e depurar — e mais
um lugar onde o span pode sumir sem aviso. O dia em que um deles aparecer, entra um coletor e
a aplicação não muda: o endereço OTLP é uma variável de ambiente.

### `zap`, e por que ele não aparece nas camadas

`zap` traz o `fxevent.ZapLogger`, que joga os eventos de boot do `fx` no mesmo log estruturado
(sem ele, erro de wiring sai cru no stderr, em outro formato, e não chega ao agregador), e o
`zaptest/observer`, que permite **afirmar sobre o que foi logado** — é como as garantias desta
seção viraram teste.

**Nenhuma camada importa `zap`** (o lint proíbe). Elas usam `observability.Field` e os
construtores re-exportados. Trocar a biblioteca de log deve ser mexer em um pacote, não em
quarenta arquivos. O mesmo vale para o **SDK** do OpenTelemetry.

### Todo erro é logado — e uma vez só

Um erro que nasce no repositório atravessa service e handler. Se cada camada logasse, um 500
viraria três linhas idênticas e a contagem de erro do painel ficaria inflada; se só a de cima
logasse, o log perderia de onde o erro veio. A saída:

- **no span**: registrado em **toda** camada — é barato e é o que desenha o caminho;
- **no log**: uma vez, na camada mais interna, a que produziu o erro.

O mecanismo é um marcador instalado uma vez por unidade de trabalho (`WithErrorTrail`, pelo
middleware HTTP e pelo `jobrunner`). Ele é um **ponteiro** no contexto justamente porque a
informação precisa andar de dentro para fora, e o contexto só propaga para dentro. A
comparação usa `errors.Is`, então um erro embrulhado continua sendo o mesmo erro. Contexto
**sem** marcador (um teste, um comando de CLI) cai no caminho seguro: loga. **Silêncio nunca é
o padrão.**

A contrapartida: **engolir um erro só é permitido via `obs.Error`**, que registra no span e no
log. É o que acontece na publicação de job que falhou.

### Correlação e desligamento gracioso

Toda linha emitida dentro de um span carrega `trace_id` e `span_id`. E
`OTEL_EXPORTER_OTLP_ENDPOINT` vazio troca os providers por no-op: o código instrumentado roda
igual, os spans só não saem do processo — é o que permite teste e dev local sem backend, com o
boot avisando que a telemetria está desligada.

---

## 12. Erros: uma biblioteca, e `errors.Is`/`errors.As` sempre

- **stdlib `errors`.** `github.com/pkg/errors` é proibido pelo lint — duas bibliotecas de erro
  no mesmo repositório significam dois jeitos de embrulhar e duas formas de comparar.
- Embrulho sempre com `fmt.Errorf("context: %w", err)`.
- **Comparação sempre com `errors.Is` / `errors.As`.** Nunca `==`, nunca `err.Error() ==`,
  nunca `strings.Contains`. O linter `errorlint` cobra as três coisas: comparação direta,
  type assertion sobre erro e `%v` onde deveria ser `%w`.
- Erro de contrato entre camadas é **sentinela exportada** pelo pacote que declara a porta
  (`<dominio>.ErrNotFound`, `auth.ErrInvalidToken`).
  É o que permite ao chamador decidir sem inspecionar mensagem — e é por isso que o handler do
  worker consegue distinguir "não tem o que fazer" de "falhou".

---

## 13. Migrações com goose

`goose` CLI, nunca migração disparada pelo código da aplicação.

Aplicação que migra sozinha no boot tem duas falhas que só aparecem em produção: duas réplicas
subindo ao mesmo tempo disputam o schema, e um rollback do deploy não desfaz o schema. Com o
goose, migrar é um passo próprio — no compose, um serviço `migrate` que roda até o fim e de
que `server` e `worker` dependem (`service_completed_successfully`), o que elimina a corrida
entre "subiu o app" e "o schema existe".

---

## 14. Testes

Duas categorias, e só duas.

### Unitário — `make test`, em segundos, sem Docker

| Pacote | O que se prova |
|---|---|
| `services/<dominio>/` | a regra de negócio, contra dublês das portas. Nenhum Postgres, SQS ou Keycloak sobe. |
| `entities/`, `structs/` | a lógica que existe ali (`Task.IsCompleted`, `Principal.HasRole`), e só ela. Struct sem comportamento não ganha teste. |

**Em mais nenhum lugar**, e isso é **cobrado pelo lint**, que proíbe importar `testing` fora
desses três caminhos. Handler que merece teste unitário é handler com lógica, e o conserto é
mover a lógica para o service. Repository é I/O: o que ele teria a provar é contra um banco de
verdade — e isso é a outra categoria, não um teste unitário com dublê de driver, que só prova
que o dublê concorda consigo mesmo.

Cobertura mínima cobrada **sobre `services/`** (`make coverage`, hoje 89%), não sobre o projeto
inteiro: um número global sobe quando alguém testa um getter e desce quando alguém escreve uma
regra — mede volume, não risco.

### Ponta a ponta — `make test-e2e`, em `app/test/`, com testcontainers

Sobe **Postgres, LocalStack (SQS) e Keycloak de verdade**, aplica as migrações e roda o
servidor e o worker no mesmo processo. Nada é dublê: é a única camada de teste que exerce os
adapters, a validação de assinatura do JWT, o SQL real e o ciclo de vida de uma mensagem.

O que ela valida (uma Feature por arquivo, listadas em [SPEC-claude.md](SPEC-claude.md)):

| Feature | O que estaria quebrado se o teste caísse |
|---|---|
| abertura de carteira, `BET`/`WIN`/`LOSS`, `REFUND`/`ROLLBACK` | a regra de negócio e o saldo |
| idempotência (HTTP, SQS, entre os dois) | dinheiro movido duas vezes |
| esquema e ledger (triggers, unicidade, `balance >= 0`) | as garantias que só o banco dá |
| `UnitOfWork` (commit, rollback, panic, aninhamento, snapshot) | atomicidade |
| outbox (N publishers, lease, backoff, crash entre publicar e confirmar) | evento perdido ou duplicado sem que o consumidor consiga distinguir |
| consumidor SQS (inbox, DLQ, retry, ack) | o contrato de entrega da fila, contra o SQS de verdade |
| referência pendente e o cronjob que a resolve | reversão que chega antes da aposta |
| leituras, cursor, reconciliação | paginação instável, saldo divergente sem alarme |
| concorrência (50 iguais, 80+80 sobre 100, instâncias independentes, HTTP × SQS) | corrida no lock da carteira |
| recuperação (kill antes do ack, retomada por outra instância, restart, SIGTERM) | um restart que inventa ou perde dinheiro |
| observabilidade e saúde (métricas, logs, `ready` 503, boot sem IdP) | painel vazio no dia em que ele é preciso |
| autenticação, papéis, doc da API | a borda |

O contrato da fila é testado **contra o SQS de verdade**, não contra um dublê: a parte que pode
estar errada é a chamada de `DeleteMessage` e o *visibility timeout*, e nenhuma das duas seria
exercida por um fake.

Cinco decisões dessa suíte:

- **Build tag `e2e`.** `make test` continua rápido e sem Docker; quem quer a suíte pede por
  ela. Suíte lenta misturada com a rápida é suíte que as pessoas param de rodar. Em troca,
  `make lint` roda duas vezes — a segunda com a tag —, porque código que ninguém linta apodrece.
- **Servidor e worker no mesmo processo.** Em produção são binários separados, mas eles se
  compõem dos mesmos módulos e registram pelas mesmas funções `ServerRoutes`/`PrepareWorker`
  que o teste chama — então o que é exercido é o registro real. O que fica de fora é só o
  ciclo de vida de cada `main`. Quando o cenário é sobre várias instâncias (corrida entre
  três servidores, publishers concorrentes) o `core` sobe **instâncias independentes**, cada uma
  com pool, verificador e memória próprios; o cenário de `SIGTERM` roda o **binário do worker**
  como processo de verdade, numa fila só dele, para que nenhum outro consumidor leve a mensagem.
  Falhas que não dá para provocar de fora (broker recusando um evento, kill entre o commit e o
  ack, dependência fora do ar no `ready`) são injetadas **nas portas** (`core/faults.go`), e tudo
  abaixo delas é o código real.
- **A máquina mora em `app/test/e2e/core/`.** Containers, migração, boot do `fx` e helpers de
  HTTP, banco e fila ficam num subpacote; os arquivos de teste não sobem nada. Um teste que
  gasta trinta linhas montando `fx` antes de afirmar qualquer coisa é um teste que ninguém lê —
  e essa montagem é idêntica em todos eles. Não há ciclo: `core` importa `app/src`, os testes
  importam `core`, e `app/src` não sabe que o pacote existe.
- **Um arquivo é uma Feature, escrita em Gherkin no comentário.** O cabeçalho declara a
  feature, o papel e os cenários; cada função declara o seu `Scenario:` em `Given/When/Then`,
  e o corpo é aquele cenário. Não é enfeite: é o único formato em que a pergunta "o que este
  teste garante?" tem resposta sem ler Go, e é o que permite revisar a *cobertura de
  comportamento* — quais cenários existem — separadamente da implementação. Quando o código e
  o cenário divergirem, o comentário é o bug.

---

## 15. O mecanismo: o que o lint cobra

| Regra | O que impede |
|---|---|
| `one-error-library` | `github.com/pkg/errors` em qualquer lugar |
| `errorlint` | `==` em erro, type assertion sobre erro, `%v` onde cabia `%w` |
| `interfaces-are-leaves` | contrato importando camada, `libs/`, driver ou framework |
| `entities-are-leaves` / `structs-are-leaves` | DTO e entidade puxando camada, pgx ou Echo |
| `services-without-infrastructure` | service importando pgx, echo, go-oidc ou `repositories` |
| `handlers-without-storage` | handler falando com banco, repositório — ou com a implementação do service |
| `repositories-without-http` | adapter conhecendo Echo, `handlers` ou `services` |
| `telemetry-libraries-stay-in-observability` | `zap` e o SDK do OTel vazando para as camadas |
| `fx` fora de `module.go` e `cmd/` | fiação misturada com regra |
| `tests-live-where-the-rules-live` | `testing` fora de `services/`, `entities/` e `structs/` |
| `fx` fora de `module.go` | fiação misturada com regra |
| `cyclop`, só em `handlers/` | regra de negócio escrita à mão dentro do handler |

Para conferir que o mecanismo funciona: adicione `import "github.com/jackc/pgx/v5"` num pacote
sob `services/` e rode `make lint`.

---

## 16. Estrutura

```
app/
├── cmd/
│   ├── server/    Echo, middlewares, ServerRoutes por domínio, lifecycle
│   └── worker/    jobrunner, PrepareWorker por domínio, probe próprio
├── src/
│   ├── interfaces/     os contratos. Folha: as três camadas apontam para cá. VAZIA hoje.
│   ├── handlers/       entrega. module.go agrega; o código vive por domínio.
│   │   ├── health/         probe do orquestrador (não é domínio)
│   │   └── identity/       quem é quem chamou (não é domínio)
│   ├── services/       regra de negócio, implementando os contratos. VAZIA hoje.
│   ├── repositories/   adapters de I/O
│   │   └── queue/          adapter de SQS, carrega bytes (não é domínio)
│   ├── entities/       espelho das tabelas. Folha. VAZIA hoje.
│   ├── structs/        o que atravessa camadas (QueueMessage, Principal, APIError)
│   └── libs/
│       ├── appinfo/        identidade da aplicação (a segregação da telemetria nasce aqui)
│       ├── bootstrap/      os módulos comuns aos dois processos
│       ├── config/         ambiente → Config, validada no boot
│       ├── observability/  OTel + zap atrás do Observer
│       ├── middleware/     autenticação e telemetria por requisição
│       ├── db/             pool pgx + ciclo de vida
│       ├── awsclients/     client do SQS
│       ├── auth/           adapter do Keycloak (verificador + service account)
│       └── jobrunner/      runtime da fila (laço, concorrência, ack, desligamento)
└── test/
    └── e2e/            testcontainers: fluxos de ponta a ponta do backend
docker/                 Dockerfile dos apps e do goose, realm do Keycloak, init do LocalStack
infra/                  Terraform: VPC, ALB, ECS Fargate, RDS, SQS
migrations/             SQL do goose
```

---

## 17. Cresce sob demanda

A disciplina é a mesma do resto: **não antecipar**. Ao sentir o sintoma, evoluir — e registrar.

| Sintoma | Evolução (ainda hexagonal) |
|---|---|
| Domínio novo (pedidos, usuários) | uma pasta em cada camada que ele toca, com `module.go` próprio |
| Services grandes, muitos casos de uso | extrair serviços de caso de uso, um por arquivo dentro do módulo do domínio |
| Mais integrações externas (pagamento, gateway) | um adapter por integração em `libs/`, atrás de uma porta. `auth/` já é um. |
| A fila precisar de ordenação ou deduplicação | FIFO no SQS: muda o adapter e a criação da fila. Nem o service nem o handler mudam. |
| Vários destinos de telemetria, ou enriquecimento fora do código | entra um coletor OTLP; a aplicação só muda de endereço |
| Muitos tipos de erro de domínio | pasta `errors/` dedicada |
| Fluxo novo que precisa de garantia | um teste em `app/test/e2e`, não um teste unitário numa camada |

---

## 18. Rodando

```bash
make up          # postgres, localstack, keycloak, observabilidade, migrate, server, worker
make token       # imprime um access token (pedro/pedro)

TOKEN=$(make -s token)
curl -s localhost:3000/health
curl -s localhost:3000/me    -H "Authorization: Bearer $TOKEN"
curl -s localhost:3000/me -H "Authorization: Bearer $TOKEN"

make logs
```

- Grafana: <http://localhost:3001> — filtre por serviço para ver a segregação; abra o trace de
  uma requisição para ver as camadas; quando houver fila com domínio, servidor e worker
  aparecem no mesmo trace.
- Keycloak: <http://localhost:8080> (`admin`/`admin`)
- Probe do worker: <http://localhost:3010/health>

```bash
make test       # unitários, com race detector
make coverage   # cobertura da regra de negócio, com piso
make lint       # é o lint que cobra as regras deste documento
make test-e2e   # fluxos de ponta a ponta, com testcontainers
```

---

## 19. Infraestrutura: Terraform em dois workspaces

O desenho está em [`infra/`](infra/README.md). **Nada foi aplicado** — é o `plan` pronto, com
contas e endereços de exemplo nos `.tfvars`.

```
                         internet
                            │
                    ┌───────▼───────┐
                    │      ALB      │  subnets PÚBLICAS, 2 AZs
                    └───────┬───────┘  o único componente com rota para a internet
                            │ :3000
        ┌───────────────────▼───────────────────┐
        │        ECS Fargate (privado)          │
        │   server  ·  worker                   │  sem IP público, saída via NAT
        └──────┬───────────────────────┬────────┘
               │ :5432
        ┌──────▼──────┐
        │ RDS Postgres│   subnets PRIVADAS, 2 AZs
        │   t4g       │
        └─────────────┘
        SQS (serviço gerenciado, alcançado via NAT)
```

### O workspace é o ambiente

Estado no S3, um arquivo por workspace (`workspaces/<sandbox|prod>/pedro-test.tfstate`). Não
existe variável que escolha o ambiente, e o workspace `default` **não é** um ambiente: uma
`precondition` derruba o plano nele.

O **sizing não está nos `.tfvars`** — ele vive em `locals.tf`, indexado pelo workspace. Instance
class, réplicas, multi-AZ, retenção de log e NAT por AZ saem de lá. Nos `.tfvars` fica só o que
de fato difere e não é tamanho: imagem, issuer do IDP, certificado, CIDR.

O motivo é que um `-var-file` errado é fácil demais de passar. Com o sizing no workspace,
aplicar a forma de produção em sandbox exigiria estar no workspace de produção — que é o mesmo
gesto que já decide para qual estado se escreve.

### As diferenças entre sandbox e prod que valem nomear

| | sandbox | prod |
|---|---|---|
| NAT | um só (ponto único de falha, e o mais barato) | um por AZ |
| RDS | `db.t4g.micro`, sem multi-AZ, backup 1 dia | `db.t4g.small`, multi-AZ, backup 14 dias |
| Réplicas | 1 de cada | 2 de cada |
| Trace | 100% amostrado | 10% |
| Destruição | permitida | `deletion_protection` no RDS e no ALB |

### O que a rede garante

- **Só o ALB tem rota para a internet.** Ele fica nas subnets públicas; ECS e RDS ficam
  nas privadas, com `assign_public_ip = false`.
- **As tasks têm saída, não entrada.** O NAT dá acesso a ECR, Secrets Manager, SQS, IDP e
  coletor; nada de fora abre conexão para elas.
- **Toda regra nomeia um security group, nunca um CIDR** — e todas moram num arquivo só
  (`security.tf`), que é o mapa de conectividade do stack. Ampliar acesso depois é nomear outro
  grupo: um ato explícito, em vez de um `/16` que ninguém relê.
- **O RDS não abre conexão para nada.** Só têm ingress, e só vindo das tasks.

### Segredos não passam pelo Terraform

- O RDS cria e rotaciona a senha do master no Secrets Manager
  (`manage_master_user_password`). A task recebe `DATABASE_PASSWORD` como *secret* do ECS, e o
  `DATABASE_URL` vai sem senha — por isso a aplicação aceita a senha separada da URL.
- O segredo do client do worker no Keycloak é criado fora e referenciado por ARN.

O efeito: o arquivo de estado não é um cofre. Quem tiver acesso de leitura ao bucket não tem,
por isso, acesso ao banco.

### Papéis separados para server e worker

O server só publica na fila (`sqs:SendMessage`); o worker só consome e apaga
(`ReceiveMessage`, `DeleteMessage`, `ChangeMessageVisibility`). São dois `task_role` distintos,
então nenhum dos dois consegue fazer o trabalho do outro — nem por engano, nem por um bug.

---

## 20. Borda: Cloudflare com WAF na frente do ALB

**Ainda não implementado. Fica registrado aqui como a decisão para a próxima etapa.**

O ALB não deve receber tráfego direto da internet. Na frente dele entra a **Cloudflare, com WAF
habilitado**, e o ALB passa a aceitar conexão apenas dos ranges da Cloudflare.

O que isso resolve, e que o ALB sozinho não resolve:

- **Ataque de camada 7 e volumetria** chegam a custar dinheiro antes de virarem indisponibilidade:
  um flood contra o ALB escala tasks e queima NAT. Filtrado na borda, ele nem entra na conta.
- **Regras gerenciadas de WAF** (OWASP core ruleset, bots conhecidos, CVEs recentes) são
  atualizadas por quem acompanha isso em tempo integral — não por nós, num PR, depois do incidente.
- **Rate limiting por rota** fica fora da aplicação. `POST` de escrita e o endpoint de token do IDP
  merecem limites diferentes do resto, e essa é uma decisão de borda, não de regra de negócio.
- **TLS e certificado** deixam de ser problema do ALB, e o certificado de origem passa a ser
  interno.
- **Acesso a rotas internas com SSO**, sem código na aplicação: é a saída para expor `/docs`
  em produção, se um dia quisermos (hoje a flag simplesmente não registra a rota).

Duas consequências que o código já precisa respeitar quando isso entrar:

1. **O IP do cliente chega em `CF-Connecting-IP`**, não no socket. Qualquer decisão baseada em
   origem (rate limit próprio, log de auditoria) tem que ler o cabeçalho — e só confiar nele
   quando a conexão vier da Cloudflare.
2. **O security group do ALB deixa de ser `0.0.0.0/0`** e passa a listar os prefixos da
   Cloudflare, mantidos por uma *prefix list* gerenciada. Sem isso, o WAF vira opcional para
   quem descobrir o DNS do ALB.

---

## 21. TO DO — entrega contínua

Nada disto existe ainda. É o próximo bloco de trabalho, e está aqui para que a ausência seja
uma decisão registrada e não um esquecimento.

### 1. CI nos PRs

Um workflow que roda em todo pull request e **barra o merge** se algo falhar:

- `make lint` — é o lint que cobra as regras de camada deste documento; sem ele no CI, elas
  viram convenção de review em uma semana.
- `make test` e `make coverage` — unitários com race detector e o piso de cobertura sobre
  `services/`.
- `make test-e2e` — a suíte de ponta a ponta com testcontainers. Ela precisa de Docker no
  runner e leva alguns minutos; é o preço de provar o caminho inteiro (Postgres, SQS, Keycloak)
  antes do merge, e não depois do deploy.
- `terraform fmt -check` e `terraform validate` em `infra/`, com o workspace selecionado.

Os quatro como *required status checks* na branch protegida. Check que não é obrigatório é
check que alguém ignora numa sexta-feira.

### 2. Revisão obrigatória do CODEOWNER

- `CODEOWNERS` mapeando caminho → time.
- Proteção de branch exigindo **pelo menos uma aprovação do code owner**, com *dismiss stale
  reviews* ligado (aprovação vale para o commit revisado, não para o que vier depois).
- Push direto na branch principal bloqueado, inclusive para administradores.

### 3. Deploy automatizado por release do GitHub

O gatilho é **publicar uma release**, não um push:

- A tag da release é a tag da imagem, então o que está em produção tem nome e changelog.
- O workflow constrói as duas imagens do mesmo `docker/Dockerfile` (mudando só `ENTRYPOINT`),
  empurra para o ECR e roda `aws ecs update-service --force-new-deployment` nos dois serviços.
- **A migração roda antes**, como uma task one-shot do goose, e um deploy que falha na migração
  não prossegue.
- Autenticação na AWS por **OIDC do GitHub**, com role por ambiente — sem chave de acesso
  guardada como secret do repositório.
- **Sandbox é automático; produção exige aprovação** (environment protection rule). O
  `deployment_circuit_breaker` do ECS já faz rollback sozinho quando as tasks novas não ficam
  saudáveis.

### 4. Cloudflare Access na doc da API, se formos expor em produção

Hoje `DOCS_ENABLED` fica desligada em produção e as rotas não existem. **Se** um dia quisermos a
página lá, a proteção não deve vir da aplicação:

- Uma policy de **Cloudflare Access** em `/docs*` e `/swagger/*`, exigindo SSO do time.
- A aplicação não muda: a flag liga a rota e quem barra é a borda, no mesmo lugar do WAF da §17.
- A alternativa — papel de realm na própria rota — tem um problema de ovo e galinha: a página
  precisa carregar *antes* de alguém conseguir pegar um token nela.

### 5. O que falta no caminho do deploy

- Repositórios ECR (hoje as imagens nos `.tfvars` são placeholders).
- Autoscaling dos serviços: do server por CPU/requisições, do worker pela profundidade da fila —
  que a aplicação já publica como métrica.
- Alarme ligado ao SNS na DLQ (o alarme existe, `alarm_actions` está vazio).

---

## 22. TO DO — cache de leitura e CDN

O Redis foi **retirado** do projeto por ora (código, compose, `.env`, testcontainers e Terraform).
Ele estava lá para uma listagem em cache-aside que este domínio não tem: as leituras de carteira,
ledger e transação vão direto ao Postgres, e o **saldo nunca é cacheado** — a única fonte dele é a
linha travada no Postgres, e um cache de saldo transformaria a reconciliação (§2) numa medida do
cache.

Fica registrado como **TO DO**, e não como decisão, que um cache pode aliviar carga do banco, junto
com cache de CDN na borda (§17), **mas exige análise maior antes de entrar**:

- **O que é seguro cachear.** Candidatos: leitura de transação já em estado terminal (`PROCESSED`
  ou `REJECTED` não mudam) e páginas do ledger fechadas por cursor (o ledger é append-only, então
  uma página cheia é imutável). Fora de cogitação: saldo, versão da carteira e qualquer coisa que a
  reconciliação compare.
- **Quanto de carga ele tira de fato.** O caminho quente é escrita (aposta, crédito, débito), que
  cache nenhum alivia. Sem medir a proporção leitura/escrita e a latência das leituras hoje, o
  ganho é hipótese.
- **Invalidação e autorização.** A resposta depende de quem lê (um provedor só vê as próprias
  transações, com 404 para as de outro): a chave de cache tem de carregar a identidade, ou o cache
  vaza dado entre provedores. Numa CDN isso pesa ainda mais.
- **Onde ficaria.** Um adapter atrás de uma porta declarada pelo service (`Cache`), em
  `repositories/`, como qualquer I/O (§1); o lint já impede um service de importar o cliente.
  Reintroduzi-lo é acrescentar um módulo, um container no compose e no e2e, e um módulo Terraform.
