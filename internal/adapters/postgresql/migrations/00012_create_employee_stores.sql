-- +goose Up
-- Many-to-many join between employees and store, sourced from Odoo's
-- x_pos_shop_ids field (see ADR-0009). Unlike employee_positions
-- (admin-writable), this table is written exclusively by SyncEmployees's
-- store-membership diff — never directly by CreateEmployee/UpdateEmployee.
CREATE TABLE employee_stores (
    employee_id BIGINT NOT NULL REFERENCES employees(id) ON DELETE CASCADE,
    store_id BIGINT NOT NULL REFERENCES store(id) ON DELETE CASCADE,
    PRIMARY KEY (employee_id, store_id)
);

CREATE INDEX idx_employee_stores_store_id ON employee_stores (store_id);

-- +goose Down
DROP TABLE employee_stores;
