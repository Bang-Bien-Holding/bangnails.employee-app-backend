-- +goose Up
-- Tracks consecutive failed Login password attempts per Employee (see issue
-- #21) so 5 in a row locks that Employee's login for 15 minutes.
-- failed_login_attempts resets to 0 on a successful password check;
-- locked_until is set only once the threshold is hit and cleared on the
-- next successful login.
ALTER TABLE employees
    ADD COLUMN failed_login_attempts INTEGER NOT NULL DEFAULT 0,
    ADD COLUMN locked_until TIMESTAMPTZ;

-- +goose Down
ALTER TABLE employees
    DROP COLUMN failed_login_attempts,
    DROP COLUMN locked_until;
