// Package orders is the domain and HTTP layer of the orders API.
package orders

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"time"
)

type Status string

const (
	Pending   Status = "pending"
	Paid      Status = "paid"
	Shipped   Status = "shipped"
	Cancelled Status = "cancelled"
)

// transitions lists, for every action, the statuses it may start from and
// the status it leads to.
var transitions = map[string]struct {
	from []Status
	to   Status
}{
	"pay":    {[]Status{Pending}, Paid},
	"ship":   {[]Status{Paid}, Shipped},
	"cancel": {[]Status{Pending, Paid}, Cancelled},
}

var (
	ErrNotFound        = errors.New("not found")
	ErrOutOfStock      = errors.New("out of stock")
	ErrBadTransition   = errors.New("transition not allowed")
	ErrKeyReused       = errors.New("idempotency key reused with a different request")
	ErrUnknownProduct  = errors.New("unknown product")
	ErrUnknownCustomer = errors.New("unknown customer")
)

// Next returns the status an action leads to from cur.
func Next(cur Status, action string) (Status, error) {
	t, ok := transitions[action]
	if !ok {
		return "", fmt.Errorf("unknown action %q", action)
	}
	for _, s := range t.from {
		if s == cur {
			return t.to, nil
		}
	}
	return "", fmt.Errorf("%w: %s from %s", ErrBadTransition, action, cur)
}

type Item struct {
	ProductID      int64 `json:"product_id"`
	Qty            int   `json:"qty"`
	UnitPriceCents int64 `json:"unit_price_cents,omitempty"`
}

type Order struct {
	ID         int64     `json:"id"`
	CustomerID int64     `json:"customer_id"`
	Status     Status    `json:"status"`
	TotalCents int64     `json:"total_cents"`
	Items      []Item    `json:"items"`
	CreatedAt  time.Time `json:"created_at"`
	UpdatedAt  time.Time `json:"updated_at"`
}

type Product struct {
	ID         int64  `json:"id"`
	SKU        string `json:"sku"`
	Name       string `json:"name"`
	PriceCents int64  `json:"price_cents"`
	Stock      int    `json:"stock"`
}

type CreateRequest struct {
	CustomerID int64  `json:"customer_id"`
	Items      []Item `json:"items"`
}

const maxQty = 1000

// Normalize validates the request, merges duplicate product lines and sorts
// items by product id. The fixed order makes concurrent orders lock product
// rows in the same order, so two multi-item orders cannot deadlock.
func (r *CreateRequest) Normalize() error {
	if r.CustomerID <= 0 {
		return errors.New("customer_id is required")
	}
	if len(r.Items) == 0 {
		return errors.New("items must not be empty")
	}
	qty := map[int64]int{}
	for _, it := range r.Items {
		if it.ProductID <= 0 {
			return errors.New("product_id is required")
		}
		if it.Qty <= 0 || it.Qty > maxQty {
			return fmt.Errorf("qty must be between 1 and %d", maxQty)
		}
		qty[it.ProductID] += it.Qty
		if qty[it.ProductID] > maxQty {
			return fmt.Errorf("qty must be between 1 and %d", maxQty)
		}
	}
	items := make([]Item, 0, len(qty))
	for id, q := range qty {
		items = append(items, Item{ProductID: id, Qty: q})
	}
	sort.Slice(items, func(i, j int) bool { return items[i].ProductID < items[j].ProductID })
	r.Items = items
	return nil
}

// Hash identifies a normalized request, so a reused idempotency key with a
// different body is rejected instead of silently returning another order.
func (r CreateRequest) Hash() string {
	b, _ := json.Marshal(r)
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}
