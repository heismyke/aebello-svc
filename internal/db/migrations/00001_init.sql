-- +goose Up
CREATE TABLE users (
    id            uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    email         text NOT NULL UNIQUE, -- stored lower-cased
    name          text NOT NULL,
    password_hash text NOT NULL,
    created_at    timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE sessions (
    id         uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id    uuid NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    token_hash bytea NOT NULL UNIQUE, -- sha256 of the bearer token
    created_at timestamptz NOT NULL DEFAULT now(),
    expires_at timestamptz NOT NULL
);
CREATE INDEX sessions_user_id_idx ON sessions (user_id);

-- One order buys one eSIM plan. Status moves
-- pending -> paid -> processing -> completed, or to failed.
CREATE TABLE orders (
    id                uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id           uuid NOT NULL REFERENCES users (id),
    status            text NOT NULL DEFAULT 'pending'
        CHECK (status IN ('pending', 'paid', 'processing', 'completed', 'failed')),
    plan_slug         text NOT NULL,
    plan_name         text NOT NULL,
    country_code      text NOT NULL DEFAULT '', -- destination the customer shopped for
    data_bytes        bigint NOT NULL,
    duration_days     int NOT NULL,
    cost_units        bigint NOT NULL, -- wholesale price, USD x 10,000 (eSIM Access units)
    amount            bigint NOT NULL, -- price charged, in minor units (cents)
    currency          text NOT NULL,
    provider_order_no text,            -- eSIM Access orderNo
    failure_reason    text,
    created_at        timestamptz NOT NULL DEFAULT now(),
    paid_at           timestamptz,
    updated_at        timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX orders_user_id_idx ON orders (user_id, created_at DESC);
CREATE INDEX orders_open_idx ON orders (status) WHERE status IN ('paid', 'processing');

CREATE TABLE esims (
    id              uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    order_id        uuid NOT NULL REFERENCES orders (id),
    user_id         uuid NOT NULL REFERENCES users (id),
    esim_tran_no    text NOT NULL UNIQUE, -- eSIM Access id for this profile
    iccid           text NOT NULL,
    activation_code text NOT NULL,        -- LPA:1$... string for manual install
    qr_code_url     text NOT NULL,
    esim_status     text NOT NULL,
    smdp_status     text NOT NULL,
    total_bytes     bigint NOT NULL,
    used_bytes      bigint NOT NULL DEFAULT 0,
    expires_at      timestamptz,
    synced_at       timestamptz NOT NULL DEFAULT now(),
    created_at      timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX esims_user_id_idx ON esims (user_id, created_at DESC);

-- +goose Down
DROP TABLE esims;
DROP TABLE orders;
DROP TABLE sessions;
DROP TABLE users;
