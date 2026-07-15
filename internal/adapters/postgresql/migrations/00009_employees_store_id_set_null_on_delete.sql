-- +goose Up
ALTER TABLE employees
    DROP CONSTRAINT employees_store_id_fkey,
    ADD CONSTRAINT employees_store_id_fkey
        FOREIGN KEY (store_id) REFERENCES store(id) ON DELETE SET NULL;

-- +goose Down
ALTER TABLE employees
    DROP CONSTRAINT employees_store_id_fkey,
    ADD CONSTRAINT employees_store_id_fkey
        FOREIGN KEY (store_id) REFERENCES store(id);
