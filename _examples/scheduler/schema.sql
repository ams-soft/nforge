-- The stock table of the scheduler example. A real application owns its
-- migrations; the scheduler never creates tables. docker-compose.yml loads this
-- file when the PostgreSQL volume is first created; to apply it by hand:
--   psql "postgres://app:app@localhost:5432/app" -f _examples/scheduler/schema.sql
CREATE TABLE IF NOT EXISTS stock (
    id         bigserial PRIMARY KEY,
    sku        text        NOT NULL UNIQUE,
    quantity   integer     NOT NULL CHECK (quantity >= 0),
    updated_at timestamptz NOT NULL DEFAULT now()
);

INSERT INTO stock (sku, quantity) VALUES
    ('SKU-0001', 10),
    ('SKU-0002', 0),
    ('SKU-0003', 25)
ON CONFLICT (sku) DO NOTHING;
