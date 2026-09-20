# Especificação Técnica: Serviço de Carteira, Wager Engine e Autenticação

## 1. Visão Geral da Arquitetura

O sistema consiste em um serviço financeiro de alta performance e consistência para carteiras e transações de apostas (*Wagering & Wallet Engine*), construído em **Go** com **Uber Fx** para injeção de dependências e estrutura modular.

### Pilares Arquiteturais
* **Banco de Dados Relacional (PostgreSQL):** Mantém o estado atual e auditável das carteiras, livro-razão (*ledger*) e histórico de transações sob garantias ACID.
* **Transactional Outbox Pattern:** Garante a consistência eventual e atômica entre o banco de dados e sistemas externos de mensageria (ex: Kafka/RabbitMQ) gravando eventos na tabela `outbox` dentro da mesma transação SQL.
* **Autenticação Descentralizada (Keycloak IdP):** Validação *offline* e de alta performance de tokens JWT (RS256) via middleware em Go com suporte a JWKS (*JSON Web Key Set*).

---

## 2. Modelo de Dados (PostgreSQL DDL)

```sql
-- 1. Carteiras dos Jogadores
CREATE TABLE wallets (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    player_id VARCHAR(64) NOT NULL UNIQUE,
    currency VARCHAR(3) NOT NULL, -- ISO 4217 (ex: BRL, USD)
    balance NUMERIC(18, 4) NOT NULL DEFAULT 0.0000,
    version BIGINT NOT NULL DEFAULT 1,
    status VARCHAR(20) NOT NULL DEFAULT 'ACTIVE', -- ACTIVE, BLOCKED, FROZEN
    created_at TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT NOW()
);

-- 2. Transações de Wager
CREATE TABLE wager_transactions (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    provider_id VARCHAR(64) NOT NULL,
    external_transaction_id VARCHAR(128) NOT NULL,
    reference_external_transaction_id VARCHAR(128),
    player_id VARCHAR(64) NOT NULL,
    wallet_id UUID NOT NULL REFERENCES wallets(id),
    kind VARCHAR(20) NOT NULL, -- OPENING, BET, WIN, LOSS, REFUND, ROLLBACK
    amount NUMERIC(18, 4) NOT NULL,
    status VARCHAR(30) NOT NULL, -- PROCESSED, REJECTED, PENDING_REFERENCE
    failure_code VARCHAR(50),     -- ex: INSUFFICIENT_FUNDS, REFUND_NOT_FOUND, ROLLBACK_INSUFFICIENT_FUNDS
    created_at TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT NOW(),
    CONSTRAINT uk_provider_ext_tx UNIQUE (provider_id, external_transaction_id)
);

-- 3. Livro-Razão (Ledger Auditável)
CREATE TABLE wallet_ledger_entries (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    wallet_id UUID NOT NULL REFERENCES wallets(id),
    wager_transaction_id UUID NOT NULL REFERENCES wager_transactions(id),
    entry_type VARCHAR(10) NOT NULL, -- CREDIT, DEBIT
    amount NUMERIC(18, 4) NOT NULL,
    balance_after NUMERIC(18, 4) NOT NULL,
    created_at TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT NOW()
);

-- 4. Outbox para Mensageria Assíncrona
CREATE TABLE outbox (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    aggregate_type VARCHAR(50) NOT NULL, -- Wallet, WagerTransaction
    aggregate_id VARCHAR(64) NOT NULL,
    event_type VARCHAR(50) NOT NULL,    -- WagerTransactionProcessed, WalletBalanceChanged, etc.
    payload JSONB NOT NULL,
    status VARCHAR(20) NOT NULL DEFAULT 'PENDING', -- PENDING, PUBLISHED, FAILED
    created_at TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT NOW(),
    processed_at TIMESTAMP WITH TIME ZONE
);

-- Índices de Otimização
CREATE INDEX idx_wager_tx_ref ON wager_transactions(provider_id, reference_external_transaction_id);
CREATE INDEX idx_outbox_pending ON outbox(status, created_at) WHERE status = 'PENDING';
```

---

## 3. Matriz de Especificação dos `kind` de Wager

| `kind` | Origem | Movimentação | Regra de Valor (`amount`) | Ledger (`wallet_ledger_entry`) | Eventos Publicados na Outbox |
| :--- | :--- | :--- | :--- | :--- | :--- |
| **`OPENING`** | Interna | Crédito | `amount > 0` | `CREDIT` (se `amount > 0`) | `WagerTransactionProcessed`<br>`WalletBalanceChanged` |
| **`BET`** | Externa | Débito | `amount > 0` | `DEBIT` | `WagerTransactionProcessed`<br>`WalletBalanceChanged` |
| **`WIN`** | Externa | Crédito | `amount > 0` | `CREDIT` | `WagerTransactionProcessed`<br>`WalletBalanceChanged` |
| **`LOSS`** | Externa | Nenhuma | Exige `amount == "0.00"` | **NENHUM** | `WagerTransactionProcessed` |
| **`REFUND`** | Externa | Crédito | `amount > 0` (Igual à `BET`) | `CREDIT` | `WagerTransactionProcessed`<br>`WalletBalanceChanged` |
| **`ROLLBACK`** | Externa | Reversão | `amount > 0` (Igual à origem) | Inverso da transação original | `WagerTransactionProcessed`<br>`WalletBalanceChanged` |

### Detalhamento e Regras de Negócio por `kind`

1. **`OPENING` (Abertura Interna de Carteira)**
   * **Restrição:** Reservado exclusivamente para rotas internas de gestão (`POST /wallets`). Rejeitado se enviado por provedores externos.
   * **Efeito:** Cria a carteira com `version = 1`. Se houver saldo inicial, gera lançamento `CREDIT` no ledger e eventos de saldo. Saldo zero não gera lançamento de ledger.

2. **`BET` (Aposta)**
   * **Validação:** Requer `balance >= amount`.
   * **Sucesso:** Debita o valor do saldo da carteira, incrementa `version`, insere `DEBIT` no ledger, marca transação como `PROCESSED` e gera eventos na Outbox.
   * **Falha:** Saldo insuficiente não altera carteira nem ledger. Marca a transação como `REJECTED` (`failureCode = "INSUFFICIENT_FUNDS"`) e emite `WagerTransactionRejected`.

3. **`WIN` (Prêmio / Vitória)**
   * **Efeito:** Incrementa o saldo da carteira, incrementa `version`, gera lançamento `CREDIT` no ledger, marca status `PROCESSED` e dispara eventos na Outbox. Pode conter referência opcional à `BET` correspondente.

4. **`LOSS` (Derrota)**
   * **Efeito:** Representa o fim de uma rodada sem premiação. Exige `amount = "0.00"`. Não altera saldo, versão nem gera registros no ledger. Persiste a transação como `PROCESSED` e gera apenas `WagerTransactionProcessed` no Outbox.

5. **`REFUND` (Devolução de Aposta)**
   * **Requisito:** Exige obrigatoriamente `referenceExternalTransactionId`.
   * **Validação:** Busca a `BET` original por `(providerId, referenceExternalTransactionId)`. Se não encontrar, salva com status `PENDING_REFERENCE` para reprocessamento assíncrono.
   * **Efeito:** Se válida, devolve o valor ao jogador (Crédito), incrementa saldo, gera ledger `CREDIT` e atualiza status para `PROCESSED`. Previne devoluções duplicadas.

6. **`ROLLBACK` (Reversão Genérica)**
   * **Requisito:** Exige `referenceExternalTransactionId`. Pode reverter `BET`, `WIN` ou `REFUND`.
   * **Comportamento Direcional:** Reverter `BET` resulta em **Crédito**. Reverter `WIN` ou `REFUND` resulta em **Débito**.
   * **Exceção de Saldo:** Se o rollback de um `WIN` exigir debitar mais do que o saldo atual do jogador, a operação é salva como `REJECTED` (`failureCode = "ROLLBACK_INSUFFICIENT_FUNDS"`).

---

## 4. Ciclo de Vida do Outbox Pattern

A escrita na tabela `outbox` ocorre **na mesma transação SQL (`BEGIN ... COMMIT`)** que atualiza o saldo e o ledger, garantando atomicidade. O payload do evento é gravado no final do bloco da transação para incluir o estado já consolidado da operação.

```text
[ DB.BeginTx() ]
  ├── 1. Locking da Carteira: SELECT ... WHERE id = $1 FOR UPDATE
  ├── 2. Validação das regras de domínio (Go Domain Layer)
  ├── 3. INSERT/UPDATE de Domínio (wallets, wager_transactions, wallet_ledger_entries)
  ├── 4. INSERT INTO outbox (Snapshot imutável com saldo final, versão e status)
[ DB.Commit() ]  <-- Tudo é tornado visível e persistido de forma totalmente atômica
```

---

## 5. Integração com Keycloak (Autenticação e Autorização)

O Keycloak atua como o **Provedor de Identidade (IdP)**. Ele não intercepta o tráfego como um proxy físico, mas fornece tokens JWT (RS256) validados por um **HTTP Middleware** dentro da aplicação Go.

### Arquitetura de Interceptação

```text
1. OBTENÇÃO DO TOKEN
   [ Game Provider ] ──( POST /token com Client ID & Secret )──> [ Keycloak ]
   [ Game Provider ] <──( Retorna Access Token JWT RS256 )────── [ Keycloak ]

2. REQUISIÇÃO PROTEGIDA
   [ Game Provider ] ──( POST /wagering/transactions com Bearer Token )──> [ Aplicação Go ]
                                                                                   │
                                                                       [ Auth Middleware ]
                                                                                   │
                                                               ├── Download JWKS do Keycloak no Startup
                                                               ├── Valida Assinatura RS256 & Exp offline
                                                               ├── Valida se provider_id bate com o Token
                                                               └── Injeta Identity no r.Context()
```

### Regras de Segurança no Middleware Go
* **Validação Offline:** A API Go baixa as chaves públicas (`JWKS`) do Keycloak na inicialização (via Uber Fx) e valida a assinatura criptográfica e expiração sem fazer chamadas HTTP adicionais ao Keycloak a cada requisição.
* **Isolamento de Tenant (`provider_id`):** O middleware compara a claim do token (ex: `provider_id`) com o payload do corpo da requisição. Divergências resultam em `403 Forbidden`.
* **Proteção de Rotas Internas (`OPENING`):** Rotas de gestão exigem a role de serviço interno no JWT (`realm_access.roles: ["internal_service"]`).

