# AI Usage

## 1. Which AI tools did I use?

Claude (Claude Code / Anthropic) and ChatGPT.

## 2. What tasks did I ask them to perform?

- Building the project's logic together: the migrations, the HTTP handlers, and the reservation/stock/idempotency business rules (transaction boundaries, row locking, expiration handling).
- Reviewing the implementation against the assignment's requirements to check nothing required was missing, and adding tests (including tests that run against a real PostgreSQL database) to prove the concurrency and reservation-lifecycle behaviour.
- Writing the `REVIEW.md` response to the provided `Reserve` code-review exercise.
- Drafting this file and parts of the README.

## 3. One AI suggestion I rejected, and why

For handling a retried request with the same `Idempotency-Key`, the first suggestion was to lock the `idempotency_keys` row before checking it, to keep two concurrent requests with the same key from racing. I rejected this because on the *first* use of a key there is no row yet to lock — two concurrent first-time requests would both find nothing and proceed anyway, so it wouldn't actually prevent a duplicate. Instead, the fix relies on the database's own `UNIQUE` constraint on `idempotency_key`: Postgres itself guarantees only one `INSERT` with a given key can succeed, and the code handles the resulting conflict by returning the reservation that did succeed.

## 4. What generated code did I substantially change or simplify?

An early version of the concurrency test asserted on timing (how long a request took) to detect lock contention. I simplified it to check only the actual outcome that matters — exactly one of several simultaneous requests succeeds, and the stock quantity ends up exactly right — since timing-based checks are unreliable and don't really prove correctness.

## 5. How did I verify that generated code was correct?

- `go build ./...`, `go vet ./...`, and `gofmt` after every change.
- The full unit test suite (`go test ./...`).
- A real, disposable PostgreSQL database (matching `docker-compose.yml`), migrated with the project's own migration files, used to run the integration tests in `internal/handler/integration_test.go` — including a test that fires concurrent reservation requests against limited stock and checks stock never goes negative or gets double-reserved. This is what actually proves the concurrency logic works, since a mocked database can't reproduce real row locking.
- Manually reading through the transaction boundaries in each handler (what runs inside vs. outside a transaction, what gets rolled back on failure) rather than assuming generated code was correct without tracing it.
