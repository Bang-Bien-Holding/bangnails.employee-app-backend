-- +goose Up
CREATE TABLE store (
    id              BIGSERIAL PRIMARY KEY,
    odoo_store_id   VARCHAR(20) UNIQUE,
    store_name      VARCHAR(255) NOT NULL,
    city            VARCHAR(255),
    latitude        NUMERIC(9,6),
    longitude       NUMERIC(9,6),
    radius_meters   INTEGER,
    is_active       BOOLEAN NOT NULL DEFAULT true,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE store_wifi_ip (
    id          BIGSERIAL PRIMARY KEY,
    store_id    BIGINT NOT NULL REFERENCES store(id) ON DELETE CASCADE,
    ip_address  INET NOT NULL,
    label       VARCHAR(100),
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (store_id, ip_address)
);

CREATE INDEX idx_store_wifi_ip_store_id ON store_wifi_ip (store_id);

CREATE TABLE store_wifi_mac (
    id          BIGSERIAL PRIMARY KEY,
    store_id    BIGINT NOT NULL REFERENCES store(id) ON DELETE CASCADE,
    mac_address MACADDR NOT NULL,
    label       VARCHAR(100),
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (store_id, mac_address)
);

CREATE INDEX idx_store_wifi_mac_store_id ON store_wifi_mac (store_id);

-- +goose Down
DROP TABLE store_wifi_mac;

DROP TABLE store_wifi_ip;

DROP TABLE store;
