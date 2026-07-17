-- +goose Up
-- Drops role and the single-store store_id entirely (their replacements —
-- positions/employee_positions and employee_stores — land in later
-- migrations). No backfill: no production employee data exists yet, so this
-- is a plain destructive change rather than a phased rename. Any existing
-- employee_id values are free-text (e.g. "M1") and can't all cast to
-- bigint, so rows are cleared up front rather than migrated in place —
-- consistent with there being no real employee data to preserve yet.
DELETE FROM employees;

ALTER TABLE employees DROP COLUMN store_id;
ALTER TABLE employees DROP COLUMN role;

ALTER TABLE employees RENAME COLUMN employee_id TO odoo_employee_id;
ALTER TABLE employees RENAME CONSTRAINT employees_employee_id_key TO employees_odoo_employee_id_key;
ALTER TABLE employees ALTER COLUMN odoo_employee_id TYPE BIGINT USING odoo_employee_id::bigint;

-- +goose Down
ALTER TABLE employees ALTER COLUMN odoo_employee_id TYPE VARCHAR(20) USING odoo_employee_id::varchar;
ALTER TABLE employees RENAME CONSTRAINT employees_odoo_employee_id_key TO employees_employee_id_key;
ALTER TABLE employees RENAME COLUMN odoo_employee_id TO employee_id;

ALTER TABLE employees ADD COLUMN role VARCHAR(50) NOT NULL DEFAULT '';
ALTER TABLE employees ADD COLUMN store_id BIGINT REFERENCES store(id) ON DELETE SET NULL;
CREATE INDEX idx_employees_store_id ON employees (store_id);
