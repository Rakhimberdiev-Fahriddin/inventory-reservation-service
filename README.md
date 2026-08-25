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

The application connects to:

```text
postgres://postgres:1@localhost:5432/inventory_db
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

The key is stored in the `idempotency_keys` table.

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

Possible errors:

```text
404 reservation not found
409 reservation cannot be confirmed
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
