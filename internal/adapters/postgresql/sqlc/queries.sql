-- name: CreateEmployee :one
INSERT INTO employees (employee_id, full_name, email, username, role)
VALUES ($1, $2, $3, $4, $5)
RETURNING *;

-- name: GetEmployeeByID :one
SELECT * FROM employees
WHERE id = $1;

-- name: GetEmployeeByEmail :one
SELECT * FROM employees
WHERE email = $1;

-- name: GetEmployeeByUsername :one
SELECT * FROM employees
WHERE username = $1;

-- name: ListEmployees :many
SELECT * FROM employees
ORDER BY id;

-- name: UpdateEmployee :one
UPDATE employees
SET employee_id = $2,
    full_name = $3,
    email = $4,
    username = $5,
    role = $6,
    updated_at = now()
WHERE id = $1
RETURNING *;

-- name: SetEmployeePassword :execrows
UPDATE employees
SET password = $2,
    updated_at = now()
WHERE id = $1;

-- name: SetEmployeeActive :execrows
UPDATE employees
SET is_active = $2,
    updated_at = now()
WHERE id = $1;

-- name: DeleteEmployee :execrows
DELETE FROM employees
WHERE id = $1;

-- name: CreatePasswordResetToken :one
INSERT INTO password_reset_tokens (employee_id, token, expires_at)
VALUES ($1, $2, $3)
RETURNING *;

-- name: GetValidPasswordResetToken :one
SELECT * FROM password_reset_tokens
WHERE token = $1
  AND used_at IS NULL
  AND expires_at > now();

-- name: MarkPasswordResetTokenUsed :exec
UPDATE password_reset_tokens
SET used_at = now()
WHERE id = $1;
