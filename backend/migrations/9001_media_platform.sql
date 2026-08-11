CREATE TABLE IF NOT EXISTS media_quotes (
    quote_id VARCHAR(128) PRIMARY KEY,
    user_id BIGINT NOT NULL REFERENCES users(id),
    api_key_id BIGINT NOT NULL REFERENCES api_keys(id),
    group_id BIGINT NOT NULL REFERENCES groups(id),
    operation VARCHAR(64) NOT NULL,
    request_json JSONB NOT NULL,
    gate_quote_token TEXT NOT NULL,
    gate_response_json JSONB NOT NULL,
    amount NUMERIC(20, 8) NOT NULL CHECK (amount > 0),
    currency VARCHAR(8) NOT NULL CHECK (currency = 'CNY'),
    expires_at TIMESTAMPTZ NOT NULL,
    consumed_order_id VARCHAR(128),
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_media_quotes_owner_expiry
    ON media_quotes(api_key_id, expires_at);

CREATE TABLE IF NOT EXISTS media_orders (
    order_id VARCHAR(128) PRIMARY KEY,
    user_id BIGINT NOT NULL REFERENCES users(id),
    api_key_id BIGINT NOT NULL REFERENCES api_keys(id),
    group_id BIGINT NOT NULL REFERENCES groups(id),
    quote_id VARCHAR(128) NOT NULL UNIQUE REFERENCES media_quotes(quote_id),
    client_idempotency_key VARCHAR(128) NOT NULL,
    gate_idempotency_key VARCHAR(128) NOT NULL UNIQUE,
    gate_execution_id VARCHAR(128),
    operation VARCHAR(64) NOT NULL,
    request_json JSONB NOT NULL,
    amount NUMERIC(20, 8) NOT NULL CHECK (amount > 0),
    currency VARCHAR(8) NOT NULL CHECK (currency = 'CNY'),
    submission_state VARCHAR(32) NOT NULL DEFAULT 'pending'
        CHECK (submission_state IN ('pending', 'submitting', 'accepted', 'unknown', 'rejected')),
    settlement_state VARCHAR(32) NOT NULL DEFAULT 'unreserved'
        CHECK (settlement_state IN ('unreserved', 'held', 'capture_pending', 'captured', 'release_pending', 'released')),
    projection_json JSONB,
    gate_response_json JSONB,
    error_code VARCHAR(128),
    error_message TEXT,
    next_reconcile_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    reconcile_attempts INTEGER NOT NULL DEFAULT 0,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    terminal_at TIMESTAMPTZ,
    UNIQUE (api_key_id, client_idempotency_key)
);

CREATE INDEX IF NOT EXISTS idx_media_orders_reconcile
    ON media_orders(next_reconcile_at, created_at)
    WHERE settlement_state IN ('unreserved', 'held', 'capture_pending', 'release_pending');
