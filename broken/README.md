# Broken variants

Three plausible regressions of `internal/orders/store.go`, each a small diff
that compiles, passes `go vet` and keeps the happy path working. Each patch
names the tests that must fail in `Expect-Fail:` lines; `make test-broken`
applies every patch, runs the suite, checks that those tests failed, and
reverts the patch (also on error or Ctrl-C).

Apply one by hand:

```bash
git apply broken/01-oversell-lost-update.patch   # break
make test                                        # watch it fail
git apply -R broken/01-oversell-lost-update.patch # restore
```

The messages below are copied from the run saved in
[`results/broken/`](../results/broken/). Counts, stock values and order ids
differ between runs; the tests that fail do not (see
[`results/catch-rate.txt`](../results/catch-rate.txt)).

## 01 — oversell by lost update

[`01-oversell-lost-update.patch`](01-oversell-lost-update.patch)

**What breaks.** `reserve()` reads the stock with `SELECT`, compares it with
the quantity in Go and writes back `stock = <computed value>`. Under READ
COMMITTED two transactions read the same stock, both pass the check, and the
second write overwrites the first. The main code does check and decrement in
one statement: `UPDATE products SET stock = stock - $qty WHERE id = $id AND
stock >= $qty`.

**Caught by** `TestConcurrentOrdersNeverOversell`: 40 clients released at
once order one unit each of a product with 5 in stock.

```
--- FAIL: TestConcurrentOrdersNeverOversell (0.59s)
    orders_test.go:250: oversold: 40 orders accepted for 5 units, 0 rejected as out of stock, 0 other errors; stock now 2
```

Every one of the 40 orders was accepted, and the stock still shows 2 units:
each write overwrote another with its own stale read.

## 02 — stock decrement outside the transaction

[`02-decrement-outside-tx.patch`](02-decrement-outside-tx.patch)

**What breaks.** One identifier: the decrement in `reserve()` goes through
`s.pool` instead of the `tx` it was given. Each decrement autocommits on
another connection, so when a later line of the order fails and the
transaction rolls back, the earlier lines' stock is already gone. Single-line
orders behave exactly as before.

**Caught by** `TestRejectedOrderLeavesNoTrace` (second line out of stock) and
`TestUnknownProductRejectedAtomically` (second line names a product that does
not exist):

```
--- FAIL: TestUnknownProductRejectedAtomically (0.02s)
    orders_test.go:287: not atomic: stock = 4 after the order with an unknown product was rejected, want 5
--- FAIL: TestRejectedOrderLeavesNoTrace (0.10s)
    orders_test.go:271: not atomic: stock of the in-stock product is 3 after the order was rejected, want 5
```

A second effect, seen in one of the first runs: every in-flight order now
needs two pool connections (its transaction plus the autocommit), so with
more concurrent orders than pool connections they all wait for each other.
That run hung `TestConcurrentOrdersNeverOversell` until the 10-minute
`go test` timeout. Since then the test client has a 10 s timeout, so a stuck
request fails the test instead of hanging the suite. Whether the pool runs dry
depends on timing, so the patch names only the two atomicity tests.

## 03 — inclusive pagination cursor

[`03-pagination-cursor-inclusive.patch`](03-pagination-cursor-inclusive.patch)

**What breaks.** `List()` filters `id >= after` instead of `id > after`.
`next_after` is the last id on a page, so that order is also the first row of
the next page. Every page still has the right size and order.

**Caught by** `TestPaginationVisitsEveryOrderOnce`, which walks all pages of
7 orders with `limit=3` and requires each order exactly once:

```
--- FAIL: TestPaginationVisitsEveryOrderOnce (0.52s)
    orders_test.go:391: order 65 returned on page 1 and again on page 2
```

With `limit=1` the same bug never ends (each page returns the cursor row
again); the test stops after 10 pages so that case fails rather than loops.
