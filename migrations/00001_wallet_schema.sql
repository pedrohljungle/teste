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
