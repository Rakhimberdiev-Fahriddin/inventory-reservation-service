# Code Review: `Reserve`

```go
func Reserve(ctx context.Context, db *sql.DB, productID int64, qty int) error {
	var available int

	err := db.QueryRowContext(
		ctx,
		`SELECT quantity FROM stock WHERE product_id = $1`,
		productID,
	).Scan(&available)
	if err != nil {
		return err
	}

	if available < qty {
		return errors.New("not enough stock")
	}

	_, err = db.ExecContext(
		ctx,
		`UPDATE stock SET quantity = quantity - $1 WHERE product_id = $2`,
		qty,
		productID,
	)
	return err
}
```

## 1. What can go wrong

- **Race condition / overselling (the critical bug).** The `SELECT` and the `UPDATE` are two separate statements, run without a transaction and without any row lock. Under concurrent load, two goroutines (or two instances of the service) can both `SELECT` the same `available` value, both pass the `available < qty` check, and both `UPDATE` the row. The stock can go negative and both callers believe they successfully reserved stock that doesn't exist. This is the exact "two concurrent requests competing for insufficient stock" scenario the assignment calls out.
- **No transaction / no atomicity.** `db *sql.DB` is used directly rather than `*sql.Tx`. If the process crashes or the context is cancelled between the `SELECT` and the `UPDATE`, there is no way to roll anything back — but more importantly, there is nothing to roll back *from*, because the check and the write aren't grouped as a single unit of work in the first place.
- **No `WHERE quantity >= $1` guard on the `UPDATE`.** Even with a transaction, the `UPDATE` blindly subtracts `qty` without re-verifying that enough stock remains at write time. It relies entirely on the earlier `SELECT`, which is stale by the time `UPDATE` runs.
- **No row existence / rows-affected check.** If `productID` doesn't exist, the `SELECT` returns `sql.ErrNoRows`, which is returned as-is — the caller sees a generic SQL error rather than a clear "product not found" error. The `UPDATE`'s result is also discarded (`_`), so if the row disappeared between the two statements, the function would return `nil` (success) despite reserving nothing.
- **No quantity validation.** A negative or zero `qty` is never rejected. `qty <= 0` would pass the `available < qty` check trivially and either do nothing useful or (for negative `qty`) *increase* stock through a "reservation" call.
- **No warehouse scoping.** The query only filters by `product_id`. If stock is tracked per warehouse (as it is in this project), this function silently operates on the wrong row, or breaks entirely under a `(warehouse_id, product_id)` uniqueness constraint.
- **This function only touches `stock` — it does not create a reservation record.** There is no way to know who reserved the quantity, undo the reservation later (cancel), or expire it. It permanently decrements stock with no corresponding, reversible business object.
- **No idempotency.** If the caller times out and retries (a scenario the assignment explicitly requires handling), `Reserve` will run twice and decrement stock twice for what the client intended as a single request.

## 2. Business-rule vs. implementation concerns

| Issue | Category |
|---|---|
| Overselling under concurrency | Business rule (violates "stock must never go negative" / "prevent overselling") |
| No reservation record created (no way to cancel/expire/track) | Business rule (the domain requires reservations, not silent stock mutation) |
| No idempotency on retry | Business rule (assignment requires idempotent reservation creation) |
| Missing quantity validation (`qty <= 0`) | Business rule (assignment requires validating client-submitted quantities) |
| No transaction wrapping SELECT+UPDATE | Implementation |
| No row lock (`FOR UPDATE`) / no atomic `WHERE quantity >= $1` | Implementation |
| Ignored `UPDATE` result / no rows-affected check | Implementation |
| `sql.ErrNoRows` leaked to caller unwrapped | Implementation |
| No warehouse scoping in the query | Implementation (reflects an incomplete data model assumption) |

## 3. How I would correct it

Wrap the check-and-decrement in a single transaction, and make the `UPDATE` itself the source of truth for whether enough stock existed — don't trust the earlier `SELECT`:

```go
func Reserve(ctx context.Context, db *sql.DB, warehouseID, productID int64, qty int) error {
	if qty <= 0 {
		return ErrInvalidQuantity
	}

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	result, err := tx.ExecContext(
		ctx,
		`UPDATE stock
		 SET quantity = quantity - $1, updated_at = NOW()
		 WHERE warehouse_id = $2 AND product_id = $3 AND quantity >= $1`,
		qty, warehouseID, productID,
	)
	if err != nil {
		return err
	}

	affected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if affected == 0 {
		// Either the row doesn't exist, or quantity < qty.
		// Distinguish the two with a follow-up existence check if the
		// caller needs to tell "not found" apart from "insufficient stock".
		return ErrInsufficientStock
	}

	return tx.Commit()
}
```

The key change is moving the sufficiency check *into* the `UPDATE`'s `WHERE` clause (`AND quantity >= $1`), so the check and the decrement happen atomically as one statement — there is no window between "read" and "write" for another transaction to interleave. This removes the need for an explicit `SELECT ... FOR UPDATE` in the simple single-row case, though `FOR UPDATE` is still the right tool when multiple rows must be checked together (as in this project's multi-item reservation, where an all-or-nothing guarantee across several products is required).

This still isn't a complete fix for the actual domain: in a real reservation system I would not decrement `stock` directly at all — I'd create a `reservations` + `reservation_items` row inside the same transaction (as this project does), so the operation is representable, cancellable, and expirable, and I'd require an idempotency key so retries are safe.

## 4. Which tests would prove the correction works

- **Insufficient stock is rejected and stock is unchanged.** Seed `quantity = 5`, call `Reserve(..., qty=10)`, assert it returns `ErrInsufficientStock` and the row still reads `5`.
- **Exact-match reservation succeeds.** Seed `quantity = 5`, reserve `5`, assert success and the row now reads `0`.
- **Negative/zero quantity is rejected** before touching the database.
- **Nonexistent product/warehouse returns a clear not-found error**, not a bare `sql.ErrNoRows` or a silent no-op.
- **Concurrency test against a real PostgreSQL instance:** seed `quantity = 10`, fire two goroutines each requesting `qty = 8` concurrently, and assert that exactly one succeeds and one fails with `ErrInsufficientStock`, and that the final stored quantity is `2` (never negative, never double-decremented). This is the test that actually proves the atomic `UPDATE ... WHERE quantity >= $1` closes the race — a test running against `sqlmock` cannot prove this, since a mock can't reproduce real lock contention.
- **Retry/idempotency test** (once the surrounding reservation logic wraps this): calling the same logical request twice with the same idempotency key decrements stock only once.

## 5. Assumptions I would confirm with a product owner

- Is stock ever meant to go negative temporarily (e.g. back-order support), or is `quantity >= 0` a hard invariant at all times? This project assumes the latter (enforced with a `CHECK (quantity >= 0)` constraint).
- Should `Reserve` fail entirely on partial stock, or is partial fulfillment (reserve what's available, backorder the rest) ever acceptable? This project assumes all-or-nothing.
- Is stock scoped per warehouse, or is there a global pool the function should fall back to if the requested warehouse is out of stock?
- What should happen if `productID` doesn't exist at all versus exists but has zero stock — should these return different error codes to the caller?
- Should a successful "reservation" here immediately and permanently reduce stock (a sale), or does it need to be reversible/expirable (a hold during checkout, as elsewhere in this project)? The original function's shape (direct, unconditional decrement with no linked record) suggests a sale, not a hold — which may not match the intended business process.
