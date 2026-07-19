-- +goose Up
-- positions is purely local — never sourced from or synced with Odoo (see
-- ADR-0008). Employee assignment (employee_positions) lands in a later
-- migration, once the join table's shape is exercised end to end.
CREATE TABLE positions (
    id          BIGSERIAL PRIMARY KEY,
    name        VARCHAR(100) NOT NULL UNIQUE,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- +goose Down
DROP TABLE positions;
