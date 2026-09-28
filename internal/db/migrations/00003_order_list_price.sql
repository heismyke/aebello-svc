-- +goose Up
-- The USD price shown to the customer. orders.amount is what was actually
-- charged, which may be in naira (see internal/fx).
ALTER TABLE orders ADD COLUMN list_price bigint NOT NULL DEFAULT 0;
UPDATE orders SET list_price = amount WHERE currency = 'USD';

-- +goose Down
ALTER TABLE orders DROP COLUMN list_price;
