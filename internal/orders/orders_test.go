package orders_test

// Integration tests: the real HTTP handler in-process, against a throwaway
// Postgres database built from db/schema.sql and db/seed.sql. Each test makes
// its own customers and products, so tests run in parallel without sharing
// rows.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/chipontix/env-kit/db"
	"github.com/chipontix/env-kit/internal/orders"
)

var (
	pool *pgxpool.Pool
	srv  *httptest.Server
	seq  atomic.Int64
)

func TestMain(m *testing.M) {
	os.Exit(run(m))
}

func run(m *testing.M) int {
	ctx := context.Background()
	admin := os.Getenv("PG_ADMIN_URL")
	if admin == "" {
		admin = "postgres://envkit:envkit@localhost:15432/postgres?sslmode=disable"
	}
	conn, err := pgx.Connect(ctx, admin)
	if err != nil {
		fmt.Fprintln(os.Stderr, "connect to Postgres (is `make up` running?):", err)
		return 1
	}
	defer conn.Close(ctx)
	name := fmt.Sprintf("envkit_test_%d_%d", os.Getpid(), time.Now().UnixNano())
	if _, err := conn.Exec(ctx, "CREATE DATABASE "+name); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	defer conn.Exec(ctx, "DROP DATABASE "+name+" WITH (FORCE)")

	cfg, err := pgxpool.ParseConfig(admin)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	cfg.ConnConfig.Database = name
	cfg.MaxConns = 32 // enough for the concurrency tests to contend for real
	pool, err = pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	defer pool.Close()
	for _, sql := range []string{db.Schema, db.Seed} {
		if _, err := pool.Exec(ctx, sql); err != nil {
			fmt.Fprintln(os.Stderr, "apply schema/seed:", err)
			return 1
		}
	}
	srv = httptest.NewServer(orders.Handler(orders.NewStore(pool), func(*http.Request) error { return nil }))
	defer srv.Close()
	return m.Run()
}

// --- helpers ---

func call(t *testing.T, method, path string, body any, key string) (int, []byte) {
	t.Helper()
	var r *bytes.Reader
	switch b := body.(type) {
	case nil:
		r = bytes.NewReader(nil)
	case string:
		r = bytes.NewReader([]byte(b))
	default:
		j, _ := json.Marshal(b)
		r = bytes.NewReader(j)
	}
	req, _ := http.NewRequest(method, srv.URL+path, r)
	if key != "" {
		req.Header.Set("Idempotency-Key", key)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	var buf bytes.Buffer
	buf.ReadFrom(res.Body)
	return res.StatusCode, buf.Bytes()
}

func decode[T any](t *testing.T, b []byte) T {
	t.Helper()
	var v T
	if err := json.Unmarshal(b, &v); err != nil {
		t.Fatalf("decode %s: %v", b, err)
	}
	return v
}

func newCustomer(t *testing.T) int64 {
	t.Helper()
	var id int64
	n := seq.Add(1)
	err := pool.QueryRow(context.Background(),
		`INSERT INTO customers (email, name) VALUES ($1, $2) RETURNING id`,
		fmt.Sprintf("%s-%d@test", t.Name(), n), t.Name()).Scan(&id)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func newProduct(t *testing.T, price int64, stock int) int64 {
	t.Helper()
	var id int64
	err := pool.QueryRow(context.Background(),
		`INSERT INTO products (sku, name, price_cents, stock) VALUES ($1, $2, $3, $4) RETURNING id`,
		fmt.Sprintf("T-%d", seq.Add(1)), t.Name(), price, stock).Scan(&id)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func stock(t *testing.T, product int64) int {
	t.Helper()
	var n int
	if err := pool.QueryRow(context.Background(), `SELECT stock FROM products WHERE id = $1`, product).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func orderCount(t *testing.T, customer int64) int {
	t.Helper()
	var n int
	if err := pool.QueryRow(context.Background(), `SELECT count(*) FROM orders WHERE customer_id = $1`, customer).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

type item = orders.Item

func order(customer int64, items ...item) orders.CreateRequest {
	return orders.CreateRequest{CustomerID: customer, Items: items}
}

// --- tests ---

func TestSeedDataIsLoaded(t *testing.T) {
	t.Parallel()
	code, body := call(t, "GET", "/products", nil, "")
	if code != 200 {
		t.Fatalf("GET /products = %d %s", code, body)
	}
	got := map[string]bool{}
	for _, p := range decode[[]orders.Product](t, body) {
		got[p.SKU] = true
	}
	for _, sku := range []string{"KB-01", "MS-01", "MN-27", "HD-01", "CB-2M", "LS-01"} {
		if !got[sku] {
			t.Errorf("seed product %s missing", sku)
		}
	}
	code, body = call(t, "GET", "/orders/1", nil, "")
	if o := decode[orders.Order](t, body); code != 200 || o.TotalCents != 11800 || len(o.Items) != 2 {
		t.Errorf("seed order 1 = %d %s, want total 11800 with 2 items", code, body)
	}
}

func TestCreateOrderComputesTotalAndTakesStock(t *testing.T) {
	t.Parallel()
	c := newCustomer(t)
	kb, cable := newProduct(t, 8900, 10), newProduct(t, 990, 10)

	// The same product twice is merged into one line.
	code, body := call(t, "POST", "/orders", order(c, item{ProductID: kb, Qty: 1}, item{ProductID: cable, Qty: 3}, item{ProductID: kb, Qty: 1}), "")
	if code != http.StatusCreated {
		t.Fatalf("POST /orders = %d %s", code, body)
	}
	o := decode[orders.Order](t, body)
	if want := int64(2*8900 + 3*990); o.TotalCents != want {
		t.Errorf("total_cents = %d, want %d", o.TotalCents, want)
	}
	if o.Status != orders.Pending || len(o.Items) != 2 {
		t.Errorf("order = %+v, want pending with 2 lines", o)
	}
	if s := stock(t, kb); s != 8 {
		t.Errorf("keyboard stock = %d, want 8", s)
	}
	if s := stock(t, cable); s != 7 {
		t.Errorf("cable stock = %d, want 7", s)
	}
}

// Many clients race for the last units. Exactly as many orders as there are
// units may succeed, and stock must end at zero, never below.
func TestConcurrentOrdersNeverOversell(t *testing.T) {
	t.Parallel()
	const units, clients = 5, 40
	c := newCustomer(t)
	p := newProduct(t, 1000, units)

	var ok, conflict, other atomic.Int64
	var wg sync.WaitGroup
	start := make(chan struct{})
	for range clients {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			code, _ := call(t, "POST", "/orders", order(c, item{ProductID: p, Qty: 1}), "")
			switch code {
			case http.StatusCreated:
				ok.Add(1)
			case http.StatusConflict:
				conflict.Add(1)
			default:
				other.Add(1)
			}
		}()
	}
	close(start)
	wg.Wait()

	if ok.Load() != units || stock(t, p) != 0 || other.Load() != 0 {
		t.Fatalf("oversold: %d orders accepted for %d units, %d rejected as out of stock, %d other errors; stock now %d",
			ok.Load(), units, conflict.Load(), other.Load(), stock(t, p))
	}
	if n := orderCount(t, c); n != units {
		t.Fatalf("%d orders stored, want %d", n, units)
	}
}

// An order whose second line is out of stock must not keep the first line's
// stock: the whole order is one transaction.
func TestRejectedOrderLeavesNoTrace(t *testing.T) {
	t.Parallel()
	c := newCustomer(t)
	inStock := newProduct(t, 500, 5)
	soldOut := newProduct(t, 700, 0)

	code, body := call(t, "POST", "/orders", order(c, item{ProductID: inStock, Qty: 2}, item{ProductID: soldOut, Qty: 1}), "")
	if code != http.StatusConflict {
		t.Fatalf("POST /orders = %d %s, want 409", code, body)
	}
	if s := stock(t, inStock); s != 5 {
		t.Fatalf("not atomic: stock of the in-stock product is %d after the order was rejected, want 5", s)
	}
	if n := orderCount(t, c); n != 0 {
		t.Fatalf("not atomic: %d orders stored after rejection, want 0", n)
	}
}

func TestUnknownProductRejectedAtomically(t *testing.T) {
	t.Parallel()
	c := newCustomer(t)
	p := newProduct(t, 500, 5)
	code, body := call(t, "POST", "/orders", order(c, item{ProductID: p, Qty: 1}, item{ProductID: 1 << 40, Qty: 1}), "")
	if code != http.StatusUnprocessableEntity {
		t.Fatalf("POST /orders = %d %s, want 422", code, body)
	}
	if s := stock(t, p); s != 5 {
		t.Fatalf("stock = %d after rejected order, want 5", s)
	}
}

func TestRetryWithSameKeyReturnsSameOrder(t *testing.T) {
	t.Parallel()
	c := newCustomer(t)
	p := newProduct(t, 1000, 10)
	req := order(c, item{ProductID: p, Qty: 2})
	key := fmt.Sprintf("retry-%d", seq.Add(1))

	code1, body1 := call(t, "POST", "/orders", req, key)
	code2, body2 := call(t, "POST", "/orders", req, key)
	if code1 != http.StatusCreated || code2 != http.StatusOK {
		t.Fatalf("codes = %d, %d, want 201 then 200 (%s / %s)", code1, code2, body1, body2)
	}
	if a, b := decode[orders.Order](t, body1).ID, decode[orders.Order](t, body2).ID; a != b {
		t.Fatalf("retry created order %d, first was %d", b, a)
	}
	if s := stock(t, p); s != 8 {
		t.Fatalf("stock = %d after a request and its retry, want 8", s)
	}
}

func TestConcurrentRetriesCreateOneOrder(t *testing.T) {
	t.Parallel()
	c := newCustomer(t)
	p := newProduct(t, 1000, 100)
	req := order(c, item{ProductID: p, Qty: 1})
	key := fmt.Sprintf("burst-%d", seq.Add(1))

	ids := make([]int64, 16)
	var wg sync.WaitGroup
	for i := range ids {
		wg.Add(1)
		go func() {
			defer wg.Done()
			code, body := call(t, "POST", "/orders", req, key)
			if code != http.StatusCreated && code != http.StatusOK {
				t.Errorf("POST = %d %s", code, body)
				return
			}
			ids[i] = decode[orders.Order](t, body).ID
		}()
	}
	wg.Wait()
	for _, id := range ids {
		if id != ids[0] {
			t.Fatalf("one key produced orders %v", ids)
		}
	}
	if n, s := orderCount(t, c), stock(t, p); n != 1 || s != 99 {
		t.Fatalf("%d orders, stock %d; want 1 order, stock 99", n, s)
	}
}

func TestKeyReusedWithDifferentBodyIsRejected(t *testing.T) {
	t.Parallel()
	c := newCustomer(t)
	p := newProduct(t, 1000, 10)
	key := fmt.Sprintf("reuse-%d", seq.Add(1))
	if code, body := call(t, "POST", "/orders", order(c, item{ProductID: p, Qty: 1}), key); code != 201 {
		t.Fatalf("first POST = %d %s", code, body)
	}
	if code, body := call(t, "POST", "/orders", order(c, item{ProductID: p, Qty: 5}), key); code != http.StatusUnprocessableEntity {
		t.Fatalf("POST with reused key and new body = %d %s, want 422", code, body)
	}
	if s := stock(t, p); s != 9 {
		t.Fatalf("stock = %d, want 9", s)
	}
}

// Walking all pages must return every order exactly once, in id order.
func TestPaginationVisitsEveryOrderOnce(t *testing.T) {
	t.Parallel()
	c := newCustomer(t)
	p := newProduct(t, 100, 100)
	var want []int64
	for range 7 {
		code, body := call(t, "POST", "/orders", order(c, item{ProductID: p, Qty: 1}), "")
		if code != 201 {
			t.Fatalf("POST = %d %s", code, body)
		}
		want = append(want, decode[orders.Order](t, body).ID)
	}

	type page struct {
		Orders    []orders.Order `json:"orders"`
		NextAfter int64          `json:"next_after"`
	}
	var got []int64
	seen := map[int64]int{}
	after := int64(0)
	for pageNo := 1; ; pageNo++ {
		if pageNo > 10 {
			t.Fatalf("pagination does not terminate: after 10 pages got ids %v", got)
		}
		code, body := call(t, "GET", fmt.Sprintf("/orders?customer_id=%d&limit=3&after=%d", c, after), nil, "")
		if code != 200 {
			t.Fatalf("GET page %d = %d %s", pageNo, code, body)
		}
		pg := decode[page](t, body)
		for _, o := range pg.Orders {
			if prev, dup := seen[o.ID]; dup {
				t.Fatalf("order %d returned on page %d and again on page %d", o.ID, prev, pageNo)
			}
			seen[o.ID] = pageNo
			got = append(got, o.ID)
		}
		if pg.NextAfter == 0 {
			break
		}
		after = pg.NextAfter
	}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("pages returned %v, want %v", got, want)
	}
}

func TestStatusTransitions(t *testing.T) {
	t.Parallel()
	c := newCustomer(t)
	p := newProduct(t, 100, 10)
	create := func() int64 {
		code, body := call(t, "POST", "/orders", order(c, item{ProductID: p, Qty: 3}), "")
		if code != 201 {
			t.Fatalf("POST = %d %s", code, body)
		}
		return decode[orders.Order](t, body).ID
	}
	step := func(id int64, action string, wantCode int, wantStatus orders.Status) {
		t.Helper()
		code, body := call(t, "POST", fmt.Sprintf("/orders/%d/%s", id, action), nil, "")
		if code != wantCode {
			t.Fatalf("%s order %d = %d %s, want %d", action, id, code, body, wantCode)
		}
		if wantStatus != "" {
			if s := decode[orders.Order](t, body).Status; s != wantStatus {
				t.Fatalf("%s order %d: status %s, want %s", action, id, s, wantStatus)
			}
		}
	}

	a := create()
	step(a, "ship", http.StatusConflict, "") // must be paid first
	step(a, "pay", 200, orders.Paid)
	step(a, "pay", http.StatusConflict, "")
	step(a, "ship", 200, orders.Shipped)
	step(a, "cancel", http.StatusConflict, "")
	if s := stock(t, p); s != 7 {
		t.Fatalf("stock = %d after shipping 3 of 10, want 7", s)
	}

	b := create()
	step(b, "pay", 200, orders.Paid)
	step(b, "cancel", 200, orders.Cancelled)
	if s := stock(t, p); s != 7 {
		t.Fatalf("stock = %d after cancelling a paid order, want 7 (stock returned)", s)
	}
	step(b, "cancel", http.StatusConflict, "") // stock is not returned twice
	if s := stock(t, p); s != 7 {
		t.Fatalf("stock = %d after a second cancel, want 7", s)
	}
	step(b, "refund", http.StatusNotFound, "")
	step(1<<40, "pay", http.StatusNotFound, "")
}

func TestRequestValidation(t *testing.T) {
	t.Parallel()
	c := newCustomer(t)
	p := newProduct(t, 100, 10)
	cases := []struct {
		name, method, path string
		body               any
		want               int
	}{
		{"malformed JSON", "POST", "/orders", `{"customer_id":`, 400},
		{"unknown field", "POST", "/orders", `{"customer_id":1,"items":[],"coupon":"X"}`, 400},
		{"no items", "POST", "/orders", order(c), 422},
		{"zero qty", "POST", "/orders", order(c, item{ProductID: p, Qty: 0}), 422},
		{"qty over limit", "POST", "/orders", order(c, item{ProductID: p, Qty: 1001}), 422},
		{"unknown customer", "POST", "/orders", order(1<<40, item{ProductID: p, Qty: 1}), 422},
		{"non-numeric id", "GET", "/orders/abc", nil, 400},
		{"missing order", "GET", "/orders/1099511627776", nil, 404},
		{"limit 0", "GET", "/orders?limit=0", nil, 400},
		{"limit too big", "GET", "/orders?limit=101", nil, 400},
		{"negative cursor", "GET", "/orders?after=-1", nil, 400},
	}
	for _, tc := range cases {
		if code, body := call(t, tc.method, tc.path, tc.body, ""); code != tc.want {
			t.Errorf("%s: %s %s = %d %s, want %d", tc.name, tc.method, tc.path, code, body, tc.want)
		}
	}
	if s := stock(t, p); s != 10 {
		t.Errorf("stock = %d after only invalid requests, want 10", s)
	}
}
