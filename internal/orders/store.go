package orders

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

type Store struct {
	pool *pgxpool.Pool
}

func NewStore(pool *pgxpool.Pool) *Store { return &Store{pool: pool} }

// Create places an order in one transaction. With a non-empty key, a repeat
// of the same request returns the original order and replayed=true.
func (s *Store) Create(ctx context.Context, req CreateRequest, key string) (o Order, replayed bool, err error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return Order{}, false, err
	}
	defer tx.Rollback(ctx)

	var id int64
	var keyArg, hashArg any
	if key != "" {
		keyArg, hashArg = key, req.Hash()
	}
	// A concurrent request with the same key blocks on the unique index until
	// the first one commits, then takes the DO NOTHING branch.
	err = tx.QueryRow(ctx, `
		INSERT INTO orders (customer_id, total_cents, idempotency_key, request_hash)
		VALUES ($1, 0, $2, $3)
		ON CONFLICT (idempotency_key) DO NOTHING
		RETURNING id`, req.CustomerID, keyArg, hashArg).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		tx.Rollback(ctx)
		return s.replay(ctx, key, req.Hash())
	}
	if isFKViolation(err) {
		return Order{}, false, ErrUnknownCustomer
	}
	if err != nil {
		return Order{}, false, err
	}

	var total int64
	for _, it := range req.Items {
		price, err := s.reserve(ctx, tx, it)
		if err != nil {
			return Order{}, false, err
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO order_items (order_id, product_id, qty, unit_price_cents)
			VALUES ($1, $2, $3, $4)`, id, it.ProductID, it.Qty, price); err != nil {
			return Order{}, false, err
		}
		total += price * int64(it.Qty)
	}
	if _, err := tx.Exec(ctx, `UPDATE orders SET total_cents = $2 WHERE id = $1`, id, total); err != nil {
		return Order{}, false, err
	}
	if err := tx.Commit(ctx); err != nil {
		return Order{}, false, err
	}
	o, err = s.Get(ctx, id)
	return o, false, err
}

// reserve takes it.Qty units of stock and returns the unit price. The check
// and the decrement are one statement, so concurrent orders cannot both see
// the last unit as free.
func (s *Store) reserve(ctx context.Context, tx pgx.Tx, it Item) (int64, error) {
	var price int64
	err := tx.QueryRow(ctx, `
		UPDATE products SET stock = stock - $2
		WHERE id = $1 AND stock >= $2
		RETURNING price_cents`, it.ProductID, it.Qty).Scan(&price)
	if errors.Is(err, pgx.ErrNoRows) {
		var exists bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM products WHERE id = $1)`, it.ProductID).Scan(&exists); err != nil {
			return 0, err
		}
		if !exists {
			return 0, ErrUnknownProduct
		}
		return 0, ErrOutOfStock
	}
	return price, err
}

func (s *Store) replay(ctx context.Context, key, hash string) (Order, bool, error) {
	var id int64
	var stored string
	err := s.pool.QueryRow(ctx, `SELECT id, request_hash FROM orders WHERE idempotency_key = $1`, key).Scan(&id, &stored)
	if err != nil {
		return Order{}, false, err
	}
	if stored != hash {
		return Order{}, false, ErrKeyReused
	}
	o, err := s.Get(ctx, id)
	return o, true, err
}

func (s *Store) Get(ctx context.Context, id int64) (Order, error) {
	var o Order
	err := s.pool.QueryRow(ctx, `
		SELECT id, customer_id, status, total_cents, created_at, updated_at
		FROM orders WHERE id = $1`, id).
		Scan(&o.ID, &o.CustomerID, &o.Status, &o.TotalCents, &o.CreatedAt, &o.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return Order{}, ErrNotFound
	}
	if err != nil {
		return Order{}, err
	}
	list := []Order{o}
	if err := s.loadItems(ctx, list); err != nil {
		return Order{}, err
	}
	return list[0], nil
}

// List returns orders with id > after in id order (keyset pagination), and
// the cursor for the next page, 0 when this is the last page.
func (s *Store) List(ctx context.Context, customerID, after int64, limit int) ([]Order, int64, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT id, customer_id, status, total_cents, created_at, updated_at
		FROM orders
		WHERE ($1 = 0 OR customer_id = $1) AND id > $2
		ORDER BY id
		LIMIT $3`, customerID, after, limit+1)
	if err != nil {
		return nil, 0, err
	}
	list, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (Order, error) {
		var o Order
		err := r.Scan(&o.ID, &o.CustomerID, &o.Status, &o.TotalCents, &o.CreatedAt, &o.UpdatedAt)
		return o, err
	})
	if err != nil {
		return nil, 0, err
	}
	var next int64
	if len(list) > limit {
		list = list[:limit]
		next = list[limit-1].ID
	}
	return list, next, s.loadItems(ctx, list)
}

func (s *Store) loadItems(ctx context.Context, list []Order) error {
	if len(list) == 0 {
		return nil
	}
	ids := make([]int64, len(list))
	byID := make(map[int64]*Order, len(list))
	for i := range list {
		ids[i] = list[i].ID
		list[i].Items = []Item{}
		byID[list[i].ID] = &list[i]
	}
	rows, err := s.pool.Query(ctx, `
		SELECT order_id, product_id, qty, unit_price_cents
		FROM order_items WHERE order_id = ANY($1)
		ORDER BY order_id, product_id`, ids)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var oid int64
		var it Item
		if err := rows.Scan(&oid, &it.ProductID, &it.Qty, &it.UnitPriceCents); err != nil {
			return err
		}
		byID[oid].Items = append(byID[oid].Items, it)
	}
	return rows.Err()
}

// Transition applies a status action. Cancelling returns the stock.
func (s *Store) Transition(ctx context.Context, id int64, action string) (Order, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return Order{}, err
	}
	defer tx.Rollback(ctx)

	var cur Status
	err = tx.QueryRow(ctx, `SELECT status FROM orders WHERE id = $1 FOR UPDATE`, id).Scan(&cur)
	if errors.Is(err, pgx.ErrNoRows) {
		return Order{}, ErrNotFound
	}
	if err != nil {
		return Order{}, err
	}
	next, err := Next(cur, action)
	if err != nil {
		return Order{}, err
	}
	if _, err := tx.Exec(ctx, `UPDATE orders SET status = $2, updated_at = now() WHERE id = $1`, id, next); err != nil {
		return Order{}, err
	}
	if next == Cancelled {
		if _, err := tx.Exec(ctx, `
			UPDATE products p SET stock = p.stock + oi.qty
			FROM order_items oi
			WHERE oi.order_id = $1 AND p.id = oi.product_id`, id); err != nil {
			return Order{}, err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return Order{}, err
	}
	return s.Get(ctx, id)
}

func (s *Store) Products(ctx context.Context) ([]Product, error) {
	rows, err := s.pool.Query(ctx, `SELECT id, sku, name, price_cents, stock FROM products ORDER BY id`)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowToStructByPos[Product])
}

func isFKViolation(err error) bool {
	var pg *pgconn.PgError
	return errors.As(err, &pg) && pg.Code == "23503"
}
