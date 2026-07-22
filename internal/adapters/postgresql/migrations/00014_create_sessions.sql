-- +goose Up
-- One active Session per Employee (see ADR-0014): employee_id is UNIQUE so
-- a new Login can atomically replace any Session already open for that
-- Employee via ON CONFLICT (employee_id) DO UPDATE, rather than a separate
-- delete-then-insert. store_id is nullable — it holds the Store Login's
-- presence check matched, and is left NULL for an Admin Session (ADR-0015),
-- not yet issued by this ticket. token_hash is a SHA-256 hex digest, never
-- the raw bearer token (same convention as password_reset_tokens.token_hash).
CREATE TABLE sessions (
    id           BIGSERIAL PRIMARY KEY,
    employee_id  BIGINT NOT NULL UNIQUE REFERENCES employees(id) ON DELETE CASCADE,
    store_id     BIGINT REFERENCES store(id) ON DELETE SET NULL,
    token_hash   VARCHAR(64) NOT NULL UNIQUE,
    issued_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    expires_at   TIMESTAMPTZ NOT NULL
);

-- +goose Down
DROP TABLE sessions;
