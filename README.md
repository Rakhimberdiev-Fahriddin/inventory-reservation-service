# Inventory Reservation Service

A backend service for managing warehouse stock and product reservations.

The service provides stock management, reservation creation, cancellation, confirmation, idempotency protection, transactional stock updates, and automatic expiration of reservations.

## Features

* Warehouse stock management
* Add stock to a warehouse
* Get product stock
* Create reservations
* Reservation items management
* Idempotency support
* Transaction-based reservation creation
* PostgreSQL row locking with `FOR UPDATE`
* Prevent reservation when stock is insufficient
* Cancel active reservations
* Restore stock when a reservation is cancelled
* Confirm active reservations
* Automatically expire reservations after 15 minutes
* Background expiration worker running every minute
* HTTP error handling with appropriate status codes

## Tech Stack

* Go
* PostgreSQL
* Docker Compose
* `database/sql`
* `pgx`
* SQL migrations
* Go `net/http`

## Project Structure

```text
inventory-reservation-service/
├── cmd/
│   └── main.go
├── internal/
│   ├── handler/
│   │   ├── reservation.go
│   │   └── stock.go
│   └── model/
│       ├── product.go
│       ├── reservation.go
│       ├── reservation_item.go
│       ├── stock.go
│       └── warehouse.go
├── migrations/
│   ├── 000001_create_products_table.up.sql
│   ├── 000002_create_warehouses_table.up.sql
│   ├── 000003_create_stock_table.up.sql
│   ├── 000004_create_reservations_table.up.sql
│   ├── 000005_create_reservation_items_table.up.sql
│   └── 000006_create_idempotency_keys_table.up.sql
├── docker-compose.yml
├── Makefile
├── go.mod
└── go.sum
```

## Database

The project uses PostgreSQL.

Start PostgreSQL with Docker Compose:

```bash
docker compose up -d
```

Check running containers:

```bash
docker compose ps
```

The application connects to (matching the credentials in `docker-compose.yml`):

```text
postgres://postgres:1@localhost:5432/inventory_db?sslmode=disable
```

This can be overridden with the `DATABASE_URL` environment variable, e.g.:

```bash
DATABASE_URL="postgres://postgres:1@localhost:5432/inventory_db?sslmode=disable" go run cmd/main.go
```

## Migrations

The database contains the following tables:

```text
products
warehouses
stock
reservations
reservation_items
idempotency_keys
```

Run the migrations using the project's Makefile command:

```bash
make migrate-up
```

To roll back migrations:

```bash
make migrate-down
```

> Migration command names depend on the commands defined in the project's `Makefile`.

## Running the Application

Start the application:

```bash
go run cmd/main.go
```

The server starts on:

```text
http://localhost:8080
```

Example output:

```text
database connected
server started on :8080
```

## API Endpoints

### Stock

#### Add Stock

```http
POST /warehouses/{warehouse_id}/stock
```

Request:

```json
{
  "product_id": 1,
  "quantity": 10
}
```

Example:

```bash
curl -X POST http://localhost:8080/warehouses/1/stock \
  -H "Content-Type: application/json" \
  -d '{
    "product_id": 1,
    "quantity": 10
  }'
```

Possible errors:

```text
400 invalid warehouse id
400 invalid request body
400 quantity must be greater than 0
404 stock not found
500 failed to update stock
```

#### Get Stock

```http
GET /warehouses/{warehouse_id}/products/{product_id}/stock
```

Example:

```bash
curl http://localhost:8080/warehouses/1/products/1/stock
```

Response:

```json
{
  "product_id": 1,
  "warehouse_id": 1,
  "physical_stock": 28,
  "available_stock": 28
}
```

Possible errors:

```text
400 invalid warehouse id
400 invalid product id
404 stock not found
500 failed to get stock
```

---

# Reservations

## Create Reservation

```http
POST /reservations
```

The request requires an `Idempotency-Key` header.

Request:

```json
{
  "warehouse_id": 1,
  "items": [
    {
      "product_id": 1,
      "quantity": 5
    }
  ]
}
```

Example:

```bash
curl -X POST http://localhost:8080/reservations \
  -H "Content-Type: application/json" \
  -H "Idempotency-Key: test-001" \
  -d '{
    "warehouse_id": 1,
    "items": [
      {
        "product_id": 1,
        "quantity": 5
      }
    ]
  }'
```

Successful response:

```json
{
  "reservation_id": 1
}
```

Status:

```text
201 Created
```

### Idempotency

The `Idempotency-Key` prevents the same reservation request from creating multiple reservations.

For example, sending the same key again:

```bash
curl -X POST http://localhost:8080/reservations \
  -H "Content-Type: application/json" \
  -H "Idempotency-Key: test-001" \
  -d '{
    "warehouse_id": 1,
    "items": [
      {
        "product_id": 1,
        "quantity": 5
      }
    ]
  }'
```

returns the existing reservation ID instead of creating another reservation.

The key is stored in the `idempotency_keys` table, with a `UNIQUE` constraint on `idempotency_key`. If two requests carrying the same key race each other, both may pass the initial lookup before either commits; the constraint guarantees only one `INSERT` succeeds, and the losing request rolls back its own reservation/stock changes and returns the winner's `reservation_id` instead of erroring. See the "Assumptions" and "Known limitations" sections below for the scope of this strategy.

## Get Reservation

```http
GET /reservations/{reservation_id}
```

Example:

```bash
curl http://localhost:8080/reservations/1
```

Response:

```json
{
  "id": 1,
  "warehouse_id": 1,
  "status": "active",
  "created_at": "2026-01-01T10:00:00Z",
  "expires_at": "2026-01-01T10:15:00Z",
  "items": [
    { "product_id": 1, "quantity": 5 }
  ]
}
```

Possible errors:

```text
400 invalid reservation id
404 reservation not found
```

## Reservation Lifecycle

A reservation starts with:

```text
active
```

It can then become:

```text
active
   ├── confirmed
   ├── cancelled
   └── expired → cancelled
```

## Cancel Reservation

```http
POST /reservations/{reservation_id}/cancel
```

Example:

```bash
curl -i -X POST http://localhost:8080/reservations/1/cancel
```

When an active reservation is cancelled:

1. Reservation is locked with `FOR UPDATE`.
2. Reservation items are read.
3. Reserved stock is restored.
4. Reservation status becomes `cancelled`.
5. The transaction is committed.

Successful response:

```text
204 No Content
```

Possible errors:

```text
404 reservation not found
409 reservation cannot be cancelled
500 failed to restore stock
500 failed to cancel reservation
```

## Confirm Reservation

```http
POST /reservations/{reservation_id}/confirm
```

Example:

```bash
curl -i -X POST http://localhost:8080/reservations/1/confirm
```

An active reservation can be confirmed.

After confirmation:

```text
active → confirmed
```

A confirmed reservation cannot be cancelled.

`ConfirmReservation` also performs a **lazy expiration check**: even if the background worker (see below) hasn't processed this reservation yet, confirming re-reads `expires_at` and, if it has already passed while the status is still `active`, restores stock, marks the reservation `cancelled`, and returns `409` instead of confirming it. This closes the gap between "expiration time reached" and "background worker noticed" — the assignment requires that "an expired reservation cannot be confirmed" and that the database is the source of truth for expiration, not the worker's schedule.

Possible errors:

```text
404 reservation not found
409 reservation cannot be confirmed
409 reservation expired
```

## Reservation Expiration

Every reservation is created with a 15-minute expiration time:

```sql
NOW() + INTERVAL '15 minutes'
```

A background worker runs every minute:

```text
1 minute
   ↓
check expired reservations
   ↓
restore reserved stock
   ↓
change status to cancelled
```

The expiration worker is started together with the HTTP server.

Example log:

```text
expire: starting
expire: transaction started
expire: found 2 expired reservations
expire: processing reservation 4
expire: restoring product=1 quantity=5
expire: processing reservation 3
expire: restoring product=1 quantity=5
expire: completed
```

## Transaction Handling

Reservation creation is performed inside a PostgreSQL transaction.

The following operations belong to the same transaction:

```text
Check idempotency key
        ↓
Check warehouse
        ↓
Check stock
        ↓
Create reservation
        ↓
Create reservation items
        ↓
Decrease stock
        ↓
Save idempotency key
        ↓
COMMIT
```

If any operation fails, the transaction is rolled back.

## Concurrent Stock Protection

Stock rows are locked using:

```sql
SELECT quantity
FROM stock
WHERE warehouse_id = $1
  AND product_id = $2
FOR UPDATE
```

This prevents concurrent reservation requests from incorrectly using the same stock.

For example, if the available stock is `10` and two requests simultaneously try to reserve `8`, PostgreSQL row locking ensures that both transactions cannot independently reserve the same `10` units.

## Testing

### Unit tests (no database required)

Handler-level unit tests use `sqlmock` to verify request validation, status-code mapping, and the exact SQL each code path issues, without a real database:

```bash
go test ./...
```

### Integration tests (real PostgreSQL required)

The assignment requires that concurrency and reservation-lifecycle behaviour be proven against a real PostgreSQL database, not a mock — a mock cannot reproduce actual row locking. `internal/handler/integration_test.go` covers:

- Multi-item reservation: insufficient stock on one item leaves *all* items unreserved (all-or-nothing).
- Two concurrent requests competing for stock that can only satisfy one of them — exactly one succeeds, stock never goes negative or gets double-decremented.
- A retried request with the same `Idempotency-Key` (including *concurrently* retried) creates exactly one reservation.
- Cancelling a reservation restores stock.
- Confirming a reservation whose `expires_at` has passed but whose background-worker cleanup hasn't run yet is rejected and stock is restored (lazy expiration).
- Invalid (negative) quantities are rejected without touching stock.

To run them:

```bash
docker compose up -d

migrate -path ./migrations \
  -database "postgres://postgres:1@localhost:5432/inventory_db?sslmode=disable" \
  up

TEST_DATABASE_URL="postgres://postgres:1@localhost:5432/inventory_db?sslmode=disable" \
  go test ./internal/handler/... -run Integration -v
```

If `TEST_DATABASE_URL` is not set, these tests are automatically skipped, so plain `go test ./...` always passes without Docker running.

## Error Status Codes

| Status                      | Meaning                                         |
| --------------------------- | ----------------------------------------------- |
| `200 OK`                    | Successful stock retrieval/update               |
| `201 Created`               | Reservation created                             |
| `204 No Content`            | Reservation cancelled successfully              |
| `400 Bad Request`           | Invalid request                                 |
| `404 Not Found`             | Warehouse, stock, or reservation not found      |
| `409 Conflict`              | Insufficient stock or invalid reservation state |
| `500 Internal Server Error` | Database or internal error                      |

## Example Error Tests

### Insufficient Stock

```bash
curl -i -X POST http://localhost:8080/reservations \
  -H "Content-Type: application/json" \
  -H "Idempotency-Key: test-error-001" \
  -d '{
    "warehouse_id": 1,
    "items": [
      {
        "product_id": 1,
        "quantity": 1000
      }
    ]
  }'
```

Expected:

```text
409 Conflict
insufficient stock
```

### Warehouse Not Found

```bash
curl -i -X POST http://localhost:8080/reservations \
  -H "Content-Type: application/json" \
  -H "Idempotency-Key: test-error-002" \
  -d '{
    "warehouse_id": 999,
    "items": [
      {
        "product_id": 1,
        "quantity": 5
      }
    ]
  }'
```

Expected:

```text
404 Not Found
warehouse not found
```

### Reservation Not Found

```bash
curl -i -X POST http://localhost:8080/reservations/999/cancel
```

Expected:

```text
404 Not Found
reservation not found
```

### Invalid Reservation State

Trying to cancel an already confirmed reservation:

```bash
curl -i -X POST http://localhost:8080/reservations/6/cancel
```

Expected:

```text
409 Conflict
reservation cannot be cancelled
```

## Assumptions

- `Idempotency-Key` is a required request header for `POST /reservations`; there is no request-body alternative.
- Available stock is modeled as `stock.quantity` decremented immediately at reservation creation and restored on cancel/expire — not as `physical_stock - SUM(active reservations)` computed on read. Both are externally equivalent as long as every code path that ends a reservation's "active" life (cancel, confirm-time lazy expiration, background worker) reliably restores stock, which is the case here.
- A reservation's items belong to exactly one warehouse; there's no cross-warehouse reservation.
- Confirming or cancelling an already-`confirmed`/`cancelled` reservation is an error (`409`), not a silent no-op — repeated *confirm*/*cancel* calls on the same terminal state are expected to fail loudly rather than succeed idempotently. (This is separate from the idempotency guarantee on *creation*, which is required by the assignment and does apply.)
- `AddStock` only increments an existing `(warehouse_id, product_id)` stock row; it does not create one. Initial stock rows are expected to be seeded directly (there's no product/warehouse-creation endpoint in scope).
- Quantities are plain integers (no fractional/weighted units).

## Important Tradeoffs

- **Stock is decremented at creation time, not at confirmation.** This means a browsing customer who creates a reservation genuinely removes stock from what other customers can see/reserve for up to 15 minutes, even if they never confirm. The alternative (decrement only at confirm) would let more customers *attempt* checkout concurrently but risks confirm-time failures after the customer thinks checkout succeeded. This project follows the assignment's definition literally (`available = physical - active reservations`) and prioritizes a strict "no overselling" guarantee over checkout-time UX.
- **Lazy expiration was added at confirm-time only, not at read-time (`GetStock`/`GetReservation`).** `GetStock` reflects the physical `stock.quantity` row, which is only corrected when a reservation is cancelled, confirmed-and-rejected-as-expired, or swept by the background worker — so immediately after an unnoticed expiration, `GET stock` can under-report availability by the expired amount until the next worker tick (at most ~1 minute). Confirm was the one place this actually mattered for correctness (the assignment explicitly forbids confirming an expired reservation), so that's where the lazy check was added rather than everywhere.
- **The idempotency race is resolved via the database's `UNIQUE` constraint plus a Postgres-specific error-code check (`23505`)**, rather than an application-level lock. This is simpler and correctly serializes concurrent creates, but ties the retry-recovery path to PostgreSQL's error reporting.
- **A background worker (1-minute ticker) is used for eventual expiration cleanup**, in addition to the confirm-time lazy check, rather than relying on lazy expiration alone everywhere. The assignment says a worker isn't required; it's kept here because `GetStock`/`GetReservation` don't otherwise self-correct, and a worker bounds how stale stock/reservation state can get.

## Known Limitations

- `GetStock` and `GetReservation` do not lazily expire on read — only `ConfirmReservation` does. A reservation can appear `active` in `GET /reservations/{id}` for up to ~1 minute past its `expires_at` before the background worker (or a confirm attempt) corrects it.
- `AddStock` cannot create a new `(warehouse_id, product_id)` stock row — there is no stock-creation/upsert endpoint, so products/warehouses/initial stock must be seeded directly in the database.
- If a duplicate `Idempotency-Key` is reused with a **different** request body (different warehouse/items/quantities), the original reservation is still returned; the new body is silently ignored rather than rejected with a conflict. The assignment doesn't require body-matching validation, but a stricter implementation would hash and compare the stored request body.
- The background expiration worker runs on every instance of the service (no leader election / distributed lock around it); with multiple instances each will attempt the same sweep every minute. This is safe (each reservation's `UPDATE` is transactional and idempotent-in-effect — a second sweep finds nothing left to expire) but does mean redundant work under horizontal scaling.
- No authentication/authorization, rate limiting, or pagination on any endpoint (explicitly out of scope per the assignment).
- No structured logging/metrics/tracing — only `log.Println`.

## What I Would Improve With More Time

- Add a stock-creation/upsert endpoint (or an explicit `POST /warehouses/{id}/products/{id}/stock` "initialize" call) instead of requiring manual seeding.
- Add lazy expiration to `GetReservation`/`GetStock` as well, so reads never show a stale `active` reservation, closing the ~1-minute staleness window described above.
- Validate that a retried `Idempotency-Key` request body matches the original (store a hash of the normalized request alongside the key) and return `409` on mismatch instead of silently returning the original reservation.
- Add a `GET /reservations?warehouse_id=&status=` listing endpoint for observability/debugging.
- Replace the polling background worker with `pg_cron` or a `SELECT ... FOR UPDATE SKIP LOCKED` batched sweep, so multiple instances don't redundantly scan the same rows every minute.
- Add structured logging (request IDs, reservation IDs) and basic metrics (reservations created/confirmed/cancelled/expired counters) for observability.

## Submission Note

- **Time spent:** approximately 6-8 hours (assignment's target range), across initial implementation and a follow-up review-and-fix pass covering the items in "Known Limitations" above.
- **Incomplete requirements:** none of the required endpoints/behaviors are missing as of the commit being submitted; see "Known Limitations" for scoped-out edge cases and "What I Would Improve" for follow-up work.
- Fill in the repository URL and commit hash being submitted here before sending this in.

## Git Commit History

The project was developed incrementally:

```text
chore: initialize Go module
chore: add PostgreSQL with Docker Compose
feat: add database migrations
feat: connect application to postgres
feat: add stock management
feat: add reservation management
feat: register handlers and reservation expiration
```

## License

This project is created for backend development and technical assessment purposes.
