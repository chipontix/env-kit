INSERT INTO customers (email, name) VALUES
    ('ada@example.com',   'Ada Lovelace'),
    ('alan@example.com',  'Alan Turing'),
    ('grace@example.com', 'Grace Hopper');

INSERT INTO products (sku, name, price_cents, stock) VALUES
    ('KB-01',  'Mechanical keyboard', 8900, 25),
    ('MS-01',  'Wireless mouse',      2900, 40),
    ('MN-27',  '27" monitor',        32900, 8),
    ('HD-01',  'USB-C hub',           4500, 30),
    ('CB-2M',  'USB-C cable 2 m',      990, 100),
    ('LS-01',  'Laptop stand',        3900, 0);

-- Orders reference the products above; stock is set directly, not derived.
INSERT INTO orders (customer_id, status, total_cents) VALUES
    (1, 'paid',      11800),
    (2, 'pending',   32900),
    (3, 'shipped',    5490),
    (1, 'cancelled',  3900);

INSERT INTO order_items (order_id, product_id, qty, unit_price_cents) VALUES
    (1, 1, 1, 8900),
    (1, 2, 1, 2900),
    (2, 3, 1, 32900),
    (3, 4, 1, 4500),
    (3, 5, 1, 990),
    (4, 6, 1, 3900);
