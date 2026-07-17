-- +goose Up
ALTER TABLE store RENAME COLUMN is_active TO wifi_whitelist_enabled;

-- +goose Down
ALTER TABLE store RENAME COLUMN wifi_whitelist_enabled TO is_active;
