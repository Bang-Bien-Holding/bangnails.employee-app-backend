-- +goose Up
-- Store a SHA-256 digest of the reset/activation token instead of the raw
-- bearer value, so a DB read (backup leak, replica access, etc.) can't be
-- used directly to redeem a token. The rename preserves the column's
-- existing NOT NULL and UNIQUE constraints.
--
-- Any outstanding (unused) rows still hold the raw bearer value, not a
-- digest, so they'd never match RedeemPasswordResetToken's post-rename
-- `WHERE token_hash = $1` lookup (which compares against SHA-256 hashes).
-- Delete them explicitly rather than leave unmatchable rows behind —
-- affected recipients just have to request a fresh reset/activation link.
DELETE FROM password_reset_tokens WHERE used_at IS NULL;

ALTER TABLE password_reset_tokens RENAME COLUMN token TO token_hash;

-- +goose Down
ALTER TABLE password_reset_tokens RENAME COLUMN token_hash TO token;
