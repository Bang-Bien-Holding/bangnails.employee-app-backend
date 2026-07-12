-- +goose Up
ALTER TABLE employees ALTER COLUMN is_active SET DEFAULT TRUE;

-- +goose Down
ALTER TABLE employees ALTER COLUMN is_active SET DEFAULT FALSE;
