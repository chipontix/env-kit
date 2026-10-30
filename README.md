# env-kit

An orders API in Go on PostgreSQL, packaged so that `make up && make test`
works on any machine with Docker, and a test suite that is itself tested:
[`broken/`](broken/) holds three realistic regressions, and CI proves on
every push that each one makes the named tests fail.

The point is not the API (it is deliberately small) but the two questions a
test suite has to answer: *does it run the same everywhere*, and *would it
notice if the code broke*.

## Quick start

Needs Docker with Compose v2 and `make`. No Go toolchain on the host: tests
run in a `golang` container against the compose Postgres.

```bash
make up            # build the API, start Postgres with schema + seed, wait until healthy
make test          # unit + integration + end-to-end, race detector
make test-broken   # apply each broken/*.patch, require its tests to fail, restore the tree
make down          # stop and delete volumes
```

`make help` lists the targets. Host ports
(default Postgres `15432`, API `15080`) are only for poking at the stack by
hand; override with `ENVKIT_PG_PORT` / `ENVKIT_API_PORT`. Tests do not use
them, they talk to `postgres:5432` and `api:8080` inside the compose network.

```bash
curl localhost:15080/products
curl -XPOST localhost:15080/orders -H 'Idempotency-Key: demo-1' \
  -d '{"customer_id":1,"items":[{"product_id":2,"qty":1}]}'
```

<details><summary><code>error getting credentials</code> on pull (macOS, colima)</summary>

If `~/.docker/config.json` says `"credsStore": "desktop"` but Docker Desktop
is not installed, every pull fails with
`docker-credential-desktop: executable file not found`. Without touching the
global config, point Docker at a config of your own:

```bash
mkdir -p ~/.docker-envkit
echo "{\"auths\":{},\"cliPluginsExtraDirs\":[\"$HOME/.docker/cli-plugins\"]}" > ~/.docker-envkit/config.json
export DOCKER_CONFIG=~/.docker-envkit
export DOCKER_HOST=unix://$HOME/.colima/default/docker.sock   # the context lived in the old config
```

`cliPluginsExtraDirs` keeps `docker compose` working; `DOCKER_HOST` replaces
the `currentContext` that was in the old config.
</details>

## What is proven, and how

| claim | evidence |
|---|---|
| A clean clone comes up and passes with two commands | `git clone` into an empty directory, `make up && make test && make test-broken`: [results/clean-clone.txt](results/clean-clone.txt); CI repeats it on every push |
| The suite catches each regression in `broken/`, on the test that names the bug | `make test-broken`: 3/3 caught, raw output in [results/broken/](results/broken/); CI job `broken` |
| …every time, not by luck (two of the bugs are races) | `make catch-rate N=50`: CATCH_RATE_LINE |
| The suite does not cry wolf | same run: CLEAN_LINE |
| `make test-broken` leaves the tree as it found it | CI runs `git diff --exit-code` after it; the script reverts in an `EXIT`/`INT`/`TERM` trap |
| No data races in the code under test | every test run uses `-race` |

## Numbers

NUMBERS_TABLE

## Architecture

```mermaid
flowchart LR
    subgraph compose["docker compose (project envkit)"]
        PG[(postgres:17<br/>schema.sql + seed.sql<br/>on first start)]
        API[api<br/>Go, net/http, pgx] --> PG
        T[test runner<br/>golang:1.26, repo mounted] -- "integration:<br/>handler in-process,<br/>throwaway database" --> PG
        T -- "e2e: HTTP" --> API
    end
    B[broken/*.patch] -. "git apply → run → git apply -R" .-> T
```

```
cmd/api/            main: config from env, pgx pool, graceful shutdown
internal/orders/    order.go   domain: status machine, request validation, request hash
                    store.go   SQL: create (one tx), reserve stock, list, transitions
                    http.go    routes and error → status mapping
                    *_test.go  unit tests and the integration suite
db/                 schema.sql, seed.sql, embedded for tests
e2e/                smoke tests against the running stack
broken/             three regressions as patches + their documentation
scripts/            test-broken.sh, catch-rate.sh
```

### API

| method | path | |
|---|---|---|
| GET | `/healthz` | 200 when the database answers |
| GET | `/products` | catalogue with stock |
| POST | `/orders` | `{"customer_id":1,"items":[{"product_id":2,"qty":1}]}`; optional `Idempotency-Key` header. 201 created, 200 replay of the same key, 409 out of stock, 422 unknown customer/product or key reused with another body |
| GET | `/orders/{id}` | order with lines |
| GET | `/orders?customer_id=&limit=&after=` | keyset pagination, `next_after` is the cursor for the next page, 0 on the last |
| POST | `/orders/{id}/pay`, `/ship`, `/cancel` | `pending → paid → shipped`, cancel from `pending` or `paid` returns the stock; anything else is 409 |

## Design decisions

- **Tests run in a container, against real Postgres.** The host needs Docker
  and make, nothing else, so "works on my machine" and "works in CI" are the
  same command. Mocks of the database would not catch two of the three bugs:
  they live in how Postgres runs concurrent transactions.
- **One throwaway database per test run, one set of rows per test.**
  `TestMain` creates `envkit_test_<pid>_<ns>` from the same `schema.sql` and
  `seed.sql` that compose loads, and drops it afterwards. Each test creates its
  own customer and products, so tests run in parallel and never depend on
  order or on what the e2e tests or a developer did to the seeded database.
- **The handler runs in-process for integration tests.** A patch from
  `broken/` is picked up by recompiling the test, no image rebuild: the whole
  `make test-broken` cycle is about ten seconds. The e2e package covers the
  built container separately.
- **Broken variants are patches, not build tags or flags.** A patch is
  exactly the diff a reviewer would see; nothing in the main code knows the
  bugs exist. Each patch says which tests must fail (`Expect-Fail:`), so the
  check is "the right test failed", not merely "something went red".
- **Measure the catch rate.** A test for a race that fails once might pass
  the next run. `make catch-rate` runs each variant N times and reports how
  often it was caught, and the clean tree N times for the flake rate.
- **Stock check and decrement are one statement**
  (`UPDATE … SET stock = stock - $q WHERE … AND stock >= $q`), not a
  `SELECT … FOR UPDATE` followed by an update: one round trip, and no window
  to forget the lock in. Order lines are sorted by product id first so two
  multi-line orders lock rows in the same order and cannot deadlock.
- **Idempotency in the database.** `idempotency_key` is `UNIQUE` and the
  order row is inserted first with `ON CONFLICT DO NOTHING`: a concurrent
  retry waits on the index and then returns the first order. The stored
  request hash rejects a reused key with a different body.
- **Keyset pagination** (`id > after`), so pages stay stable while orders are
  being inserted, which `OFFSET` does not.
- **Money in integer cents**, never floats.

## Limitations

- No authentication, no payments: `pay` only moves the status.
- Idempotency keys are global and kept forever; a real service would scope
  them per client and expire them.
- The seed is loaded by Postgres' init scripts, i.e. only into an empty
  volume. `make reset` gets back to seed data. There are no migrations for a
  schema change on existing data.
- Three bugs are a sample, not a mutation-testing score; a tool such as a
  mutation tester would find surviving mutants this suite does not kill.

## Reproducing the numbers

REPRO

## Third-party code

| | license |
|---|---|
| [jackc/pgx](https://github.com/jackc/pgx) v5 and its deps puddle, pgpassfile, pgservicefile | MIT |
| golang.org/x/text, golang.org/x/sync (indirect, via pgx) | BSD-3-Clause |
| Docker images `postgres:17-alpine`, `golang:1.26-bookworm`, `alpine:3.22` | PostgreSQL License, BSD-3-Clause, MIT (base layers carry their own package licenses) |

## License

MIT, see [LICENSE](LICENSE).
