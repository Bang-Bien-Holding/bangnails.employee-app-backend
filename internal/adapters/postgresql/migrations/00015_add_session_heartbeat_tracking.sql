-- +goose Up
-- Tracks Heartbeat's own state per Session (issue #23): last_heartbeat_at is
-- the silence backstop's clock (no heartbeat, pass or fail, for 90s expires
-- the Session) and defaults to now() so a freshly-issued Session starts its
-- silence window at Login, not at some later first heartbeat.
-- consecutive_failures counts unbroken failed presence rechecks; two in a
-- row ends the Session (ADR-0014). Both reset to their defaults on every
-- (re)issued Login via UpsertSession's ON CONFLICT — a new Session, even for
-- an Employee who had one open before, starts with a clean slate.
ALTER TABLE sessions
    ADD COLUMN last_heartbeat_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    ADD COLUMN consecutive_failures INTEGER NOT NULL DEFAULT 0;

-- +goose Down
ALTER TABLE sessions
    DROP COLUMN last_heartbeat_at,
    DROP COLUMN consecutive_failures;
