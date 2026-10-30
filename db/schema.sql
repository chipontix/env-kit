CREATE TABLE customers (
    id    bigserial PRIMARY KEY,
    email text NOT NULL UNIQUE,
    name  text NOT NULL
);

CREATE TABLE products (
    id          bigserial PRIMARY KEY,
    sku         text   NOT NULL UNIQUE,
    name        text   NOT NULL,
    price_cents bigint NOT NULL CHECK (price_cents > 0),
    stock       int    NOT NULL CHECK (stock >= 0)
);

CREATE TYPE order_status AS ENUM ('pending', 'paid', 'shipped', 'cancelled');

CREATE TABLE orders (
    id              bigserial PRIMARY KEY,
    customer_id     bigint       NOT NULL REFERENCES customers (id),
    status          order_status NOT NULL DEFAULT 'pending',
    total_cents     bigint       NOT NULL CHECK (total_cents >= 0),
    -- A retried POST /orders with the same key returns the first order.
    idempotency_key text UNIQUE,
    request_hash    text,
    created_at      timestamptz  NOT NULL DEFAULT now(),
    updated_at      timestamptz  NOT NULL DEFAULT now()
);

CREATE INDEX orders_customer_id_id ON orders (customer_id, id);

CREATE TABLE order_items (
    order_id         bigint NOT NULL REFERENCES orders (id) ON DELETE CASCADE,
    product_id       bigint NOT NULL REFERENCES products (id),
    qty              int    NOT NULL CHECK (qty > 0),
    unit_price_cents bigint NOT NULL,
    PRIMARY KEY (order_id, product_id)
);
