-- +goose Up
-- Store a SHA-256 digest of the reset/activation token instead of the raw
-- bearer value, so a DB read (backup leak, replica access, etc.) can't be
-- used directly to redeem a token. The rename preserves the column's
-- existing NOT NULL and UNIQUE constraints.
ALTER TABLE password_reset_tokens RENAME COLUMN token TO token_hash;

-- +goose Down
ALTER TABLE password_reset_tokens RENAME COLUMN token_hash TO token;
