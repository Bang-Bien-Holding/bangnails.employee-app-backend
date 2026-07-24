-- +goose Up
-- Records every incoming POST /v1/password-reset-requests (issue #39),
-- throttled to 3/email/15min and 10/ip/15min. Rows older than the current
-- window are opportunistically deleted on each write, so this table never
-- grows unbounded and needs no separate cleanup job.
CREATE TABLE password_reset_requests (
    id         BIGSERIAL PRIMARY KEY,
    ip_address INET        NOT NULL,
    email      TEXT        NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_password_reset_requests_email_created_at ON password_reset_requests (email, created_at);
CREATE INDEX idx_password_reset_requests_ip_address_created_at ON password_reset_requests (ip_address, created_at);

-- +goose Down
DROP TABLE password_reset_requests;
