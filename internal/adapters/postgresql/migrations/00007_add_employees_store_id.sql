-- +goose Up
ALTER TABLE employees
    ADD COLUMN store_id BIGINT REFERENCES store(id) ON DELETE SET NULL;

CREATE INDEX idx_employees_store_id ON employees (store_id);

-- +goose Down
DROP INDEX idx_employees_store_id;

ALTER TABLE employees
    DROP COLUMN store_id;
