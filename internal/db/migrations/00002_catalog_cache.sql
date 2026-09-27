-- +goose Up
-- The last plan list fetched from eSIM Access (one row). Downloading the full
-- list takes over a minute, so on start-up the API serves this copy while it
-- refreshes in the background.
CREATE TABLE catalog_cache (
    id         int PRIMARY KEY DEFAULT 1 CHECK (id = 1),
    packages   jsonb NOT NULL,
    fetched_at timestamptz NOT NULL DEFAULT now()
);

-- +goose Down
DROP TABLE catalog_cache;
