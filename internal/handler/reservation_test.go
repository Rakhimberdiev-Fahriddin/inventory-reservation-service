package handler

import (
	"context"
	"database/sql"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
)

func TestValidateCreateReservationRequest(t *testing.T) {
	tests := []struct {
		name    string
		req     CreateReservationRequest
		wantErr bool
	}{
		{
			name: "valid request",
			req: CreateReservationRequest{
				WarehouseID: 1,
				Items: []CreateReservationItemRequest{
					{
						ProductID: 1,
						Quantity:  5,
					},
				},
			},
			wantErr: false,
		},
		{
			name: "invalid warehouse",
			req: CreateReservationRequest{
				WarehouseID: 0,
				Items: []CreateReservationItemRequest{
					{
						ProductID: 1,
						Quantity:  5,
					},
				},
			},
			wantErr: true,
		},
		{
			name: "empty items",
			req: CreateReservationRequest{
				WarehouseID: 1,
				Items:       nil,
			},
			wantErr: true,
		},
		{
			name: "invalid product",
			req: CreateReservationRequest{
				WarehouseID: 1,
				Items: []CreateReservationItemRequest{
					{
						ProductID: 0,
						Quantity:  5,
					},
				},
			},
			wantErr: true,
		},
		{
			name: "invalid quantity",
			req: CreateReservationRequest{
				WarehouseID: 1,
				Items: []CreateReservationItemRequest{
					{
						ProductID: 1,
						Quantity:  0,
					},
				},
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateCreateReservationRequest(tt.req)

			if (err != nil) != tt.wantErr {
				t.Errorf(
					"validateCreateReservationRequest() error = %v, wantErr = %v",
					err,
					tt.wantErr,
				)
			}
		})
	}
}

func TestCreateReservation(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	h := NewHandler(db)

	mock.ExpectBegin()

	mock.ExpectQuery("SELECT reservation_id").
		WithArgs("test-001").
		WillReturnError(sql.ErrNoRows)

	mock.ExpectQuery("SELECT EXISTS").
		WithArgs(int64(1)).
		WillReturnRows(
			sqlmock.NewRows([]string{"exists"}).
				AddRow(true),
		)

	mock.ExpectQuery("SELECT quantity").
		WithArgs(int64(1), int64(1)).
		WillReturnRows(
			sqlmock.NewRows([]string{"quantity"}).
				AddRow(10),
		)

	mock.ExpectQuery("INSERT INTO reservations").
		WithArgs(int64(1)).
		WillReturnRows(
			sqlmock.NewRows([]string{"id"}).
				AddRow(int64(1)),
		)

	mock.ExpectExec("INSERT INTO reservation_items").
		WithArgs(int64(1), int64(1), 5).
		WillReturnResult(
			sqlmock.NewResult(1, 1),
		)

	mock.ExpectExec("UPDATE stock").
		WithArgs(5, int64(1), int64(1)).
		WillReturnResult(
			sqlmock.NewResult(1, 1),
		)

	mock.ExpectExec("INSERT INTO idempotency_keys").
		WithArgs("test-001", int64(1)).
		WillReturnResult(
			sqlmock.NewResult(1, 1),
		)

	mock.ExpectCommit()

	req := httptest.NewRequest(
		http.MethodPost,
		"/reservations",
		strings.NewReader(`{
			"warehouse_id": 1,
			"items": [
				{
					"product_id": 1,
					"quantity": 5
				}
			]
		}`),
	)

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Idempotency-Key", "test-001")

	rec := httptest.NewRecorder()

	h.CreateReservation(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf(
			"expected status %d, got %d",
			http.StatusCreated,
			rec.Code,
		)
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestCreateReservation_InsufficientStock(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	h := NewHandler(db)

	mock.ExpectBegin()

	mock.ExpectQuery("SELECT reservation_id").
		WithArgs("test-insufficient-001").
		WillReturnError(sql.ErrNoRows)

	mock.ExpectQuery("SELECT EXISTS").
		WithArgs(int64(1)).
		WillReturnRows(
			sqlmock.NewRows([]string{"exists"}).
				AddRow(true),
		)

	mock.ExpectQuery("SELECT quantity").
		WithArgs(int64(1), int64(1)).
		WillReturnRows(
			sqlmock.NewRows([]string{"quantity"}).
				AddRow(3),
		)

	mock.ExpectRollback()

	req := httptest.NewRequest(
		http.MethodPost,
		"/reservations",
		strings.NewReader(`{
			"warehouse_id": 1,
			"items": [
				{
					"product_id": 1,
					"quantity": 5
				}
			]
		}`),
	)

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Idempotency-Key", "test-insufficient-001")

	rec := httptest.NewRecorder()

	h.CreateReservation(rec, req)

	if rec.Code != http.StatusConflict {
		t.Fatalf(
			"expected status %d, got %d",
			http.StatusConflict,
			rec.Code,
		)
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestCreateReservation_WarehouseNotFound(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	h := NewHandler(db)

	mock.ExpectBegin()

	mock.ExpectQuery("SELECT reservation_id").
		WithArgs("test-warehouse-001").
		WillReturnError(sql.ErrNoRows)

	mock.ExpectQuery("SELECT EXISTS").
		WithArgs(int64(999)).
		WillReturnRows(
			sqlmock.NewRows([]string{"exists"}).
				AddRow(false),
		)

	mock.ExpectRollback()

	req := httptest.NewRequest(
		http.MethodPost,
		"/reservations",
		strings.NewReader(`{
			"warehouse_id": 999,
			"items": [
				{
					"product_id": 1,
					"quantity": 5
				}
			]
		}`),
	)

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Idempotency-Key", "test-warehouse-001")

	rec := httptest.NewRecorder()

	h.CreateReservation(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf(
			"expected status %d, got %d",
			http.StatusNotFound,
			rec.Code,
		)
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestCreateReservation_Idempotency(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	h := NewHandler(db)

	mock.ExpectBegin()

	mock.ExpectQuery("SELECT reservation_id").
		WithArgs("test-idempotency-001").
		WillReturnRows(
			sqlmock.NewRows([]string{"reservation_id"}).
				AddRow(int64(10)),
		)

	mock.ExpectRollback()

	req := httptest.NewRequest(
		http.MethodPost,
		"/reservations",
		strings.NewReader(`{
			"warehouse_id": 1,
			"items": [
				{
					"product_id": 1,
					"quantity": 5
				}
			]
		}`),
	)

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Idempotency-Key", "test-idempotency-001")

	rec := httptest.NewRecorder()

	h.CreateReservation(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf(
			"expected status %d, got %d",
			http.StatusCreated,
			rec.Code,
		)
	}

	expectedResponse := `{"reservation_id":10}`

	if strings.TrimSpace(rec.Body.String()) != expectedResponse {
		t.Fatalf(
			"expected response %s, got %s",
			expectedResponse,
			strings.TrimSpace(rec.Body.String()),
		)
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestCancelReservation_Success(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	h := NewHandler(db)

	mock.ExpectBegin()

	mock.ExpectQuery("SELECT warehouse_id, status").
		WithArgs(int64(1)).
		WillReturnRows(
			sqlmock.NewRows([]string{"warehouse_id", "status"}).
				AddRow(int64(1), "active"),
		)

	mock.ExpectQuery("SELECT product_id, quantity").
		WithArgs(int64(1)).
		WillReturnRows(
			sqlmock.NewRows([]string{"product_id", "quantity"}).
				AddRow(int64(1), 5),
		)

	mock.ExpectExec("UPDATE stock").
		WithArgs(5, int64(1), int64(1)).
		WillReturnResult(
			sqlmock.NewResult(1, 1),
		)

	mock.ExpectExec("UPDATE reservations").
		WithArgs(int64(1)).
		WillReturnResult(
			sqlmock.NewResult(1, 1),
		)

	mock.ExpectCommit()

	req := httptest.NewRequest(
		http.MethodPost,
		"/reservations/1/cancel",
		nil,
	)

	req.SetPathValue("reservation_id", "1")

	rec := httptest.NewRecorder()

	h.CancelReservation(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Fatalf(
			"expected status %d, got %d",
			http.StatusNoContent,
			rec.Code,
		)
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestCancelReservation_Confirmed(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	h := NewHandler(db)

	mock.ExpectBegin()

	mock.ExpectQuery("SELECT warehouse_id, status").
		WithArgs(int64(1)).
		WillReturnRows(
			sqlmock.NewRows([]string{"warehouse_id", "status"}).
				AddRow(int64(1), "confirmed"),
		)

	mock.ExpectRollback()

	req := httptest.NewRequest(
		http.MethodPost,
		"/reservations/1/cancel",
		nil,
	)

	req.SetPathValue("reservation_id", "1")

	rec := httptest.NewRecorder()

	h.CancelReservation(rec, req)

	if rec.Code != http.StatusConflict {
		t.Fatalf(
			"expected status %d, got %d",
			http.StatusConflict,
			rec.Code,
		)
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestCancelReservation_NotFound(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	h := NewHandler(db)

	mock.ExpectBegin()

	mock.ExpectQuery("SELECT warehouse_id, status").
		WithArgs(int64(999)).
		WillReturnError(sql.ErrNoRows)

	mock.ExpectRollback()

	req := httptest.NewRequest(
		http.MethodPost,
		"/reservations/999/cancel",
		nil,
	)

	req.SetPathValue("reservation_id", "999")

	rec := httptest.NewRecorder()

	h.CancelReservation(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf(
			"expected status %d, got %d",
			http.StatusNotFound,
			rec.Code,
		)
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestConfirmReservation_Success(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	h := NewHandler(db)

	mock.ExpectBegin()

	mock.ExpectQuery("SELECT status").
		WithArgs(int64(1)).
		WillReturnRows(
			sqlmock.NewRows([]string{"status"}).
				AddRow("active"),
		)

	mock.ExpectExec("UPDATE reservations").
		WithArgs(int64(1)).
		WillReturnResult(
			sqlmock.NewResult(1, 1),
		)

	mock.ExpectCommit()

	req := httptest.NewRequest(
		http.MethodPost,
		"/reservations/1/confirm",
		nil,
	)

	req.SetPathValue("reservation_id", "1")

	rec := httptest.NewRecorder()

	h.ConfirmReservation(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Fatalf(
			"expected status %d, got %d",
			http.StatusNoContent,
			rec.Code,
		)
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestConfirmReservation_Cancelled(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	h := NewHandler(db)

	mock.ExpectBegin()

	mock.ExpectQuery("SELECT status").
		WithArgs(int64(1)).
		WillReturnRows(
			sqlmock.NewRows([]string{"status"}).
				AddRow("cancelled"),
		)

	mock.ExpectRollback()

	req := httptest.NewRequest(
		http.MethodPost,
		"/reservations/1/confirm",
		nil,
	)

	req.SetPathValue("reservation_id", "1")

	rec := httptest.NewRecorder()

	h.ConfirmReservation(rec, req)

	if rec.Code != http.StatusConflict {
		t.Fatalf(
			"expected status %d, got %d",
			http.StatusConflict,
			rec.Code,
		)
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestConfirmReservation_NotFound(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	h := NewHandler(db)

	mock.ExpectBegin()

	mock.ExpectQuery("SELECT status").
		WithArgs(int64(999)).
		WillReturnError(sql.ErrNoRows)

	mock.ExpectRollback()

	req := httptest.NewRequest(
		http.MethodPost,
		"/reservations/999/confirm",
		nil,
	)

	req.SetPathValue("reservation_id", "999")

	rec := httptest.NewRecorder()

	h.ConfirmReservation(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf(
			"expected status %d, got %d",
			http.StatusNotFound,
			rec.Code,
		)
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestExpireReservations_Success(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	h := NewHandler(db)

	mock.ExpectBegin()

	mock.ExpectQuery("SELECT id, warehouse_id").
		WillReturnRows(
			sqlmock.NewRows([]string{"id", "warehouse_id"}).
				AddRow(int64(1), int64(1)),
		)

	mock.ExpectQuery("SELECT product_id, quantity").
		WithArgs(int64(1)).
		WillReturnRows(
			sqlmock.NewRows([]string{"product_id", "quantity"}).
				AddRow(int64(1), 5),
		)

	mock.ExpectExec("UPDATE stock").
		WithArgs(5, int64(1), int64(1)).
		WillReturnResult(
			sqlmock.NewResult(1, 1),
		)

	mock.ExpectExec("UPDATE reservations").
		WithArgs(int64(1)).
		WillReturnResult(
			sqlmock.NewResult(1, 1),
		)

	mock.ExpectCommit()

	err = h.ExpireReservations(context.Background())
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestExpireReservations_NoExpiredReservations(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	h := NewHandler(db)

	mock.ExpectBegin()

	mock.ExpectQuery("SELECT id, warehouse_id").
		WillReturnRows(
			sqlmock.NewRows([]string{"id", "warehouse_id"}),
		)

	mock.ExpectCommit()

	err = h.ExpireReservations(context.Background())
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}
