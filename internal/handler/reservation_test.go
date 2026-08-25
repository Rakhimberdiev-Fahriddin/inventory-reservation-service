package handler

import (
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

	// Idempotency key mavjud emas.
	mock.ExpectQuery("SELECT reservation_id").
		WithArgs("test-001").
		WillReturnError(sql.ErrNoRows)

	// Warehouse mavjud.
	mock.ExpectQuery("SELECT EXISTS").
		WithArgs(int64(1)).
		WillReturnRows(
			sqlmock.NewRows([]string{"exists"}).
				AddRow(true),
		)

	// Stock mavjud va yetarli.
	mock.ExpectQuery("SELECT quantity").
		WithArgs(int64(1), int64(1)).
		WillReturnRows(
			sqlmock.NewRows([]string{"quantity"}).
				AddRow(10),
		)

	// Reservation yaratildi.
	mock.ExpectQuery("INSERT INTO reservations").
		WithArgs(int64(1)).
		WillReturnRows(
			sqlmock.NewRows([]string{"id"}).
				AddRow(int64(1)),
		)

	// Reservation item yaratildi.
	mock.ExpectExec("INSERT INTO reservation_items").
		WithArgs(int64(1), int64(1), 5).
		WillReturnResult(
			sqlmock.NewResult(1, 1),
		)

	// Stock kamaytirildi.
	mock.ExpectExec("UPDATE stock").
		WithArgs(5, int64(1), int64(1)).
		WillReturnResult(
			sqlmock.NewResult(1, 1),
		)

	// Idempotency key saqlandi.
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

	// Idempotency key mavjud emas.
	mock.ExpectQuery("SELECT reservation_id").
		WithArgs("test-insufficient-001").
		WillReturnError(sql.ErrNoRows)

	// Warehouse mavjud.
	mock.ExpectQuery("SELECT EXISTS").
		WithArgs(int64(1)).
		WillReturnRows(
			sqlmock.NewRows([]string{"exists"}).
				AddRow(true),
		)

	// Stock mavjud, lekin quantity yetarli emas.
	mock.ExpectQuery("SELECT quantity").
		WithArgs(int64(1), int64(1)).
		WillReturnRows(
			sqlmock.NewRows([]string{"quantity"}).
				AddRow(3),
		)

	// Transaction rollback bo'lishi kerak.
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
