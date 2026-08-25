package handler

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	_ "github.com/jackc/pgx/v5/stdlib"
)

func openTestDB(t *testing.T) *sql.DB {
	t.Helper()

	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set; skipping PostgreSQL integration test")
	}

	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	if err := db.Ping(); err != nil {
		t.Fatalf("ping db: %v", err)
	}

	return db
}

func resetSchema(t *testing.T, db *sql.DB) {
	t.Helper()

	_, err := db.Exec(`
		TRUNCATE TABLE idempotency_keys, reservation_items, reservations, stock, warehouses, products
		RESTART IDENTITY CASCADE
	`)
	if err != nil {
		t.Fatalf("truncate tables: %v", err)
	}
}

func seedProductWarehouseStock(t *testing.T, db *sql.DB, quantity int) (productID, warehouseID int64) {
	t.Helper()

	if err := db.QueryRow(`INSERT INTO products (name) VALUES ('widget') RETURNING id`).Scan(&productID); err != nil {
		t.Fatalf("seed product: %v", err)
	}

	if err := db.QueryRow(`INSERT INTO warehouses (name) VALUES ('main') RETURNING id`).Scan(&warehouseID); err != nil {
		t.Fatalf("seed warehouse: %v", err)
	}

	if _, err := db.Exec(
		`INSERT INTO stock (warehouse_id, product_id, quantity) VALUES ($1, $2, $3)`,
		warehouseID, productID, quantity,
	); err != nil {
		t.Fatalf("seed stock: %v", err)
	}

	return productID, warehouseID
}

func TestIntegration_CreateReservation_MultiItem_AllOrNothing(t *testing.T) {
	db := openTestDB(t)
	resetSchema(t, db)

	h := NewHandler(db)

	productA, warehouseID := seedProductWarehouseStock(t, db, 10)

	var productB int64
	if err := db.QueryRow(`INSERT INTO products (name) VALUES ('gadget') RETURNING id`).Scan(&productB); err != nil {
		t.Fatalf("seed product B: %v", err)
	}

	if _, err := db.Exec(
		`INSERT INTO stock (warehouse_id, product_id, quantity) VALUES ($1, $2, 2)`,
		warehouseID, productB,
	); err != nil {
		t.Fatalf("seed stock B: %v", err)
	}

	body := fmt.Sprintf(`{
		"warehouse_id": %d,
		"items": [
			{"product_id": %d, "quantity": 5},
			{"product_id": %d, "quantity": 5}
		]
	}`, warehouseID, productA, productB)

	req := httptest.NewRequest(http.MethodPost, "/reservations", strings.NewReader(body))
	req.Header.Set("Idempotency-Key", "multi-item-key")
	rec := httptest.NewRecorder()

	h.CreateReservation(rec, req)

	if rec.Code != http.StatusConflict {
		t.Fatalf("expected 409, got %d: %s", rec.Code, rec.Body.String())
	}

	var qtyA, qtyB int
	if err := db.QueryRow(`SELECT quantity FROM stock WHERE product_id = $1`, productA).Scan(&qtyA); err != nil {
		t.Fatalf("read stock A: %v", err)
	}
	if err := db.QueryRow(`SELECT quantity FROM stock WHERE product_id = $1`, productB).Scan(&qtyB); err != nil {
		t.Fatalf("read stock B: %v", err)
	}

	if qtyA != 10 {
		t.Errorf("product A stock changed despite failed reservation: got %d, want 10", qtyA)
	}
	if qtyB != 2 {
		t.Errorf("product B stock changed: got %d, want 2", qtyB)
	}

	var reservationCount int
	if err := db.QueryRow(`SELECT COUNT(*) FROM reservations`).Scan(&reservationCount); err != nil {
		t.Fatalf("count reservations: %v", err)
	}
	if reservationCount != 0 {
		t.Errorf("expected no reservation to be persisted, found %d", reservationCount)
	}
}

func TestIntegration_ConcurrentReservations_NoOverselling(t *testing.T) {
	db := openTestDB(t)
	resetSchema(t, db)

	h := NewHandler(db)

	productID, warehouseID := seedProductWarehouseStock(t, db, 10)

	const concurrency = 2
	const requestedQty = 8

	var wg sync.WaitGroup
	var successCount int32

	for i := 0; i < concurrency; i++ {
		wg.Add(1)

		go func(i int) {
			defer wg.Done()

			body := fmt.Sprintf(`{
				"warehouse_id": %d,
				"items": [{"product_id": %d, "quantity": %d}]
			}`, warehouseID, productID, requestedQty)

			req := httptest.NewRequest(http.MethodPost, "/reservations", strings.NewReader(body))
			req.Header.Set("Idempotency-Key", fmt.Sprintf("concurrent-key-%d", i))
			rec := httptest.NewRecorder()

			h.CreateReservation(rec, req)

			if rec.Code == http.StatusCreated {
				atomic.AddInt32(&successCount, 1)
			}
		}(i)
	}

	wg.Wait()

	if successCount != 1 {
		t.Fatalf("expected exactly 1 successful reservation out of %d concurrent requests competing for insufficient stock, got %d", concurrency, successCount)
	}

	var remaining int
	if err := db.QueryRow(`SELECT quantity FROM stock WHERE product_id = $1`, productID).Scan(&remaining); err != nil {
		t.Fatalf("read stock: %v", err)
	}

	if remaining != 10-requestedQty {
		t.Fatalf("stock went inconsistent under concurrency: got %d, want %d (never negative, never double-reserved)", remaining, 10-requestedQty)
	}

	if remaining < 0 {
		t.Fatalf("stock went negative: %d", remaining)
	}
}

func TestIntegration_IdempotentRetry_SameKey_SameResult(t *testing.T) {
	db := openTestDB(t)
	resetSchema(t, db)

	h := NewHandler(db)

	productID, warehouseID := seedProductWarehouseStock(t, db, 10)

	makeRequest := func() *httptest.ResponseRecorder {
		body := fmt.Sprintf(`{
			"warehouse_id": %d,
			"items": [{"product_id": %d, "quantity": 5}]
		}`, warehouseID, productID)

		req := httptest.NewRequest(http.MethodPost, "/reservations", strings.NewReader(body))
		req.Header.Set("Idempotency-Key", "retry-key")
		rec := httptest.NewRecorder()

		h.CreateReservation(rec, req)
		return rec
	}

	first := makeRequest()
	if first.Code != http.StatusCreated {
		t.Fatalf("first request: expected 201, got %d: %s", first.Code, first.Body.String())
	}

	var firstResp map[string]int64
	if err := json.Unmarshal(first.Body.Bytes(), &firstResp); err != nil {
		t.Fatalf("decode first response: %v", err)
	}

	second := makeRequest()
	if second.Code != http.StatusCreated {
		t.Fatalf("retry request: expected 201, got %d: %s", second.Code, second.Body.String())
	}

	var secondResp map[string]int64
	if err := json.Unmarshal(second.Body.Bytes(), &secondResp); err != nil {
		t.Fatalf("decode retry response: %v", err)
	}

	if firstResp["reservation_id"] != secondResp["reservation_id"] {
		t.Fatalf("retry created a different reservation: first=%d second=%d", firstResp["reservation_id"], secondResp["reservation_id"])
	}

	var reservationCount int
	if err := db.QueryRow(`SELECT COUNT(*) FROM reservations`).Scan(&reservationCount); err != nil {
		t.Fatalf("count reservations: %v", err)
	}
	if reservationCount != 1 {
		t.Fatalf("expected exactly 1 reservation after retry, found %d", reservationCount)
	}

	var remaining int
	if err := db.QueryRow(`SELECT quantity FROM stock WHERE product_id = $1`, productID).Scan(&remaining); err != nil {
		t.Fatalf("read stock: %v", err)
	}
	if remaining != 5 {
		t.Fatalf("stock decremented more than once by a retried request: got %d, want 5", remaining)
	}
}

func TestIntegration_ConcurrentIdempotentRetry_NoDuplicateReservation(t *testing.T) {
	db := openTestDB(t)
	resetSchema(t, db)

	h := NewHandler(db)

	productID, warehouseID := seedProductWarehouseStock(t, db, 10)

	const concurrency = 5

	var wg sync.WaitGroup
	responses := make([]int, concurrency)

	for i := 0; i < concurrency; i++ {
		wg.Add(1)

		go func(i int) {
			defer wg.Done()

			body := fmt.Sprintf(`{
				"warehouse_id": %d,
				"items": [{"product_id": %d, "quantity": 5}]
			}`, warehouseID, productID)

			req := httptest.NewRequest(http.MethodPost, "/reservations", strings.NewReader(body))
			req.Header.Set("Idempotency-Key", "same-key-race")
			rec := httptest.NewRecorder()

			h.CreateReservation(rec, req)
			responses[i] = rec.Code
		}(i)
	}

	wg.Wait()

	for i, code := range responses {
		if code != http.StatusCreated {
			t.Errorf("request %d: expected 201, got %d", i, code)
		}
	}

	var reservationCount int
	if err := db.QueryRow(`SELECT COUNT(*) FROM reservations`).Scan(&reservationCount); err != nil {
		t.Fatalf("count reservations: %v", err)
	}
	if reservationCount != 1 {
		t.Fatalf("expected exactly 1 reservation despite %d concurrent requests with the same idempotency key, found %d", concurrency, reservationCount)
	}

	var remaining int
	if err := db.QueryRow(`SELECT quantity FROM stock WHERE product_id = $1`, productID).Scan(&remaining); err != nil {
		t.Fatalf("read stock: %v", err)
	}
	if remaining != 5 {
		t.Fatalf("stock decremented more than once under a concurrent idempotency race: got %d, want 5", remaining)
	}
}

func TestIntegration_CancelReservation_RestoresStock(t *testing.T) {
	db := openTestDB(t)
	resetSchema(t, db)

	h := NewHandler(db)

	productID, warehouseID := seedProductWarehouseStock(t, db, 10)

	body := fmt.Sprintf(`{
		"warehouse_id": %d,
		"items": [{"product_id": %d, "quantity": 5}]
	}`, warehouseID, productID)

	createReq := httptest.NewRequest(http.MethodPost, "/reservations", strings.NewReader(body))
	createReq.Header.Set("Idempotency-Key", "cancel-key")
	createRec := httptest.NewRecorder()
	h.CreateReservation(createRec, createReq)

	if createRec.Code != http.StatusCreated {
		t.Fatalf("create: expected 201, got %d: %s", createRec.Code, createRec.Body.String())
	}

	var created map[string]int64
	if err := json.Unmarshal(createRec.Body.Bytes(), &created); err != nil {
		t.Fatalf("decode create response: %v", err)
	}
	reservationID := created["reservation_id"]

	cancelReq := httptest.NewRequest(http.MethodPost, fmt.Sprintf("/reservations/%d/cancel", reservationID), nil)
	cancelReq.SetPathValue("reservation_id", fmt.Sprintf("%d", reservationID))
	cancelRec := httptest.NewRecorder()
	h.CancelReservation(cancelRec, cancelReq)

	if cancelRec.Code != http.StatusNoContent {
		t.Fatalf("cancel: expected 204, got %d: %s", cancelRec.Code, cancelRec.Body.String())
	}

	var remaining int
	if err := db.QueryRow(`SELECT quantity FROM stock WHERE product_id = $1`, productID).Scan(&remaining); err != nil {
		t.Fatalf("read stock: %v", err)
	}
	if remaining != 10 {
		t.Fatalf("stock not restored after cancel: got %d, want 10", remaining)
	}

	var status string
	if err := db.QueryRow(`SELECT status FROM reservations WHERE id = $1`, reservationID).Scan(&status); err != nil {
		t.Fatalf("read reservation status: %v", err)
	}
	if status != "cancelled" {
		t.Fatalf("expected status cancelled, got %q", status)
	}
}

func TestIntegration_ConfirmAfterExpiration_Rejected(t *testing.T) {
	db := openTestDB(t)
	resetSchema(t, db)

	h := NewHandler(db)

	productID, warehouseID := seedProductWarehouseStock(t, db, 10)

	var reservationID int64
	if err := db.QueryRow(
		`INSERT INTO reservations (warehouse_id, status, expires_at)
		 VALUES ($1, 'active', NOW() - INTERVAL '1 minute')
		 RETURNING id`,
		warehouseID,
	).Scan(&reservationID); err != nil {
		t.Fatalf("seed expired reservation: %v", err)
	}

	if _, err := db.Exec(
		`INSERT INTO reservation_items (reservation_id, product_id, quantity) VALUES ($1, $2, 5)`,
		reservationID, productID,
	); err != nil {
		t.Fatalf("seed reservation item: %v", err)
	}
	if _, err := db.Exec(
		`UPDATE stock SET quantity = quantity - 5 WHERE product_id = $1`,
		productID,
	); err != nil {
		t.Fatalf("seed decremented stock: %v", err)
	}

	confirmReq := httptest.NewRequest(http.MethodPost, fmt.Sprintf("/reservations/%d/confirm", reservationID), nil)
	confirmReq.SetPathValue("reservation_id", fmt.Sprintf("%d", reservationID))
	confirmRec := httptest.NewRecorder()
	h.ConfirmReservation(confirmRec, confirmReq)

	if confirmRec.Code != http.StatusConflict {
		t.Fatalf("expected 409 for confirming an expired reservation, got %d: %s", confirmRec.Code, confirmRec.Body.String())
	}

	var status string
	if err := db.QueryRow(`SELECT status FROM reservations WHERE id = $1`, reservationID).Scan(&status); err != nil {
		t.Fatalf("read reservation status: %v", err)
	}
	if status != "cancelled" {
		t.Fatalf("expected lazily-expired reservation to be marked cancelled, got %q", status)
	}

	var remaining int
	if err := db.QueryRow(`SELECT quantity FROM stock WHERE product_id = $1`, productID).Scan(&remaining); err != nil {
		t.Fatalf("read stock: %v", err)
	}
	if remaining != 10 {
		t.Fatalf("expected stock restored by lazy expiration, got %d, want 10", remaining)
	}
}

func TestIntegration_InvalidQuantity_Rejected(t *testing.T) {
	db := openTestDB(t)
	resetSchema(t, db)

	h := NewHandler(db)

	productID, warehouseID := seedProductWarehouseStock(t, db, 10)

	body := fmt.Sprintf(`{
		"warehouse_id": %d,
		"items": [{"product_id": %d, "quantity": -5}]
	}`, warehouseID, productID)

	req := httptest.NewRequest(http.MethodPost, "/reservations", strings.NewReader(body))
	req.Header.Set("Idempotency-Key", "invalid-qty-key")
	rec := httptest.NewRecorder()

	h.CreateReservation(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for negative quantity, got %d: %s", rec.Code, rec.Body.String())
	}

	var remaining int
	if err := db.QueryRow(`SELECT quantity FROM stock WHERE product_id = $1`, productID).Scan(&remaining); err != nil {
		t.Fatalf("read stock: %v", err)
	}
	if remaining != 10 {
		t.Fatalf("stock changed despite invalid request: got %d, want 10", remaining)
	}
}
