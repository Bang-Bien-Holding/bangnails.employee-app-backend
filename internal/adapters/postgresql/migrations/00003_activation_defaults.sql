-- +goose Up
ALTER TABLE employees ALTER COLUMN is_active SET DEFAULT FALSE;
ALTER TABLE employees DROP COLUMN must_change_password;

-- +goose Down
ALTER TABLE employees ADD COLUMN must_change_password BOOLEAN NOT NULL DEFAULT TRUE;
ALTER TABLE employees ALTER COLUMN is_active SET DEFAULT TRUE;
