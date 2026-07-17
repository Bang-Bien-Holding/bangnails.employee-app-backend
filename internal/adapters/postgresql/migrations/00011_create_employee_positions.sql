-- +goose Up
-- Many-to-many join between employees and positions (see ADR-0008). Unlike
-- employee_stores (Odoo-owned, lands separately), this table is written
-- exclusively by CreateEmployee/UpdateEmployee via a whole-set diff replace.
CREATE TABLE employee_positions (
    employee_id BIGINT NOT NULL REFERENCES employees(id) ON DELETE CASCADE,
    position_id BIGINT NOT NULL REFERENCES positions(id) ON DELETE CASCADE,
    PRIMARY KEY (employee_id, position_id)
);

CREATE INDEX idx_employee_positions_position_id ON employee_positions (position_id);

-- +goose Down
DROP TABLE employee_positions;
