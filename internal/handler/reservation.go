package handler

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
)

type CreateReservationRequest struct {
	WarehouseID int64                          `json:"warehouse_id"`
	Items       []CreateReservationItemRequest `json:"items"`
}

type CreateReservationItemRequest struct {
	ProductID int64 `json:"product_id"`
	Quantity  int   `json:"quantity"`
}

var ErrInsufficientStock = errors.New("insufficient stock")

type ReservationItemResponse struct {
	ProductID int64 `json:"product_id"`
	Quantity  int   `json:"quantity"`
}

type ReservationResponse struct {
	ID          int64                     `json:"id"`
	WarehouseID int64                     `json:"warehouse_id"`
	Status      string                    `json:"status"`
	CreatedAt   time.Time                 `json:"created_at"`
	ExpiresAt   time.Time                 `json:"expires_at"`
	Items       []ReservationItemResponse `json:"items"`
}

func (h *Handler) GetReservation(w http.ResponseWriter, r *http.Request) {
	reservationID, err := strconv.ParseInt(
		r.PathValue("reservation_id"),
		10,
		64,
	)

	if err != nil {
		http.Error(w, "invalid reservation id", http.StatusBadRequest)
		return
	}

	var resp ReservationResponse
	resp.ID = reservationID

	err = h.db.QueryRowContext(
		r.Context(),
		`
		SELECT warehouse_id, status, created_at, expires_at
		FROM reservations
		WHERE id = $1
		`,
		reservationID,
	).Scan(&resp.WarehouseID, &resp.Status, &resp.CreatedAt, &resp.ExpiresAt)

	if errors.Is(err, sql.ErrNoRows) {
		http.Error(w, "reservation not found", http.StatusNotFound)
		return
	}

	if err != nil {
		http.Error(w, "failed to get reservation", http.StatusInternalServerError)
		return
	}

	rows, err := h.db.QueryContext(
		r.Context(),
		`
		SELECT product_id, quantity
		FROM reservation_items
		WHERE reservation_id = $1
		`,
		reservationID,
	)

	if err != nil {
		http.Error(w, "failed to get reservation items", http.StatusInternalServerError)
		return
	}
	defer rows.Close()

	for rows.Next() {
		var item ReservationItemResponse

		if err := rows.Scan(&item.ProductID, &item.Quantity); err != nil {
			http.Error(w, "failed to read reservation item", http.StatusInternalServerError)
			return
		}

		resp.Items = append(resp.Items, item)
	}

	if err := rows.Err(); err != nil {
		http.Error(w, "failed to read reservation items", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(resp)
}

func (h *Handler) CreateReservation(w http.ResponseWriter, r *http.Request) {

	idempotencyKey := r.Header.Get("Idempotency-Key")

	if idempotencyKey == "" {
		http.Error(w, "idempotency key is required", http.StatusBadRequest)
		return
	}

	var req CreateReservationRequest

	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}

	if err := validateCreateReservationRequest(req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	tx, err := h.db.BeginTx(r.Context(), nil)

	if err != nil {
		http.Error(w, "failed to start transaction", http.StatusInternalServerError)
		return
	}

	defer tx.Rollback()

	var existingReservationID int64

	err = tx.QueryRowContext(
		r.Context(),
		`
	SELECT reservation_id
	FROM idempotency_keys
	WHERE idempotency_key = $1
	`,
		idempotencyKey,
	).Scan(&existingReservationID)

	if err == nil {
		response := map[string]int64{
			"reservation_id": existingReservationID,
		}

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)

		json.NewEncoder(w).Encode(response)
		return
	}

	if err != sql.ErrNoRows {
		http.Error(w, "failed to check idempotency key", http.StatusInternalServerError)
		return
	}

	if err := h.checkWarehouse(
		r.Context(),
		tx,
		req.WarehouseID,
	); err != nil {

		if errors.Is(err, sql.ErrNoRows) {
			http.Error(w, "warehouse not found", http.StatusNotFound)
			return
		}

		http.Error(w, "failed to check warehouse", http.StatusInternalServerError)
		return
	}

	if err := h.checkStock(
		r.Context(),
		tx,
		req,
	); err != nil {

		if errors.Is(err, sql.ErrNoRows) {
			http.Error(w, "stock not found", http.StatusNotFound)
			return
		}

		if errors.Is(err, ErrInsufficientStock) {
			http.Error(w, "insufficient stock", http.StatusConflict)
			return
		}

		http.Error(w, "failed to check stock", http.StatusInternalServerError)
		return
	}

	reservationID, err := h.createReservation(
		r.Context(),
		tx,
		req.WarehouseID,
	)
	if err != nil {
		http.Error(w, "failed to create reservation", http.StatusInternalServerError)
		return
	}

	if err := h.createReservationItems(
		r.Context(),
		tx,
		reservationID,
		req.Items,
	); err != nil {
		http.Error(w, "failed to create reservation item", http.StatusInternalServerError)
		return
	}

	if err := h.decreaseStock(
		r.Context(),
		tx,
		req,
	); err != nil {
		http.Error(w, "failed to update stock", http.StatusInternalServerError)
		return
	}

	_, err = tx.ExecContext(
		r.Context(),
		`
	INSERT INTO idempotency_keys (
		idempotency_key,
		reservation_id
	)
	VALUES ($1, $2)
	`,
		idempotencyKey,
		reservationID,
	)

	if err != nil {

		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			tx.Rollback()

			var winnerReservationID int64

			lookupErr := h.db.QueryRowContext(
				r.Context(),
				`SELECT reservation_id FROM idempotency_keys WHERE idempotency_key = $1`,
				idempotencyKey,
			).Scan(&winnerReservationID)

			if lookupErr != nil {
				http.Error(w, "failed to save idempotency key", http.StatusInternalServerError)
				return
			}

			response := map[string]int64{
				"reservation_id": winnerReservationID,
			}

			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusCreated)
			json.NewEncoder(w).Encode(response)
			return
		}

		http.Error(w, "failed to save idempotency key", http.StatusInternalServerError)
		return
	}

	if err := tx.Commit(); err != nil {
		http.Error(w, "failed to commit transaction", http.StatusInternalServerError)
		return
	}

	response := map[string]int64{
		"reservation_id": reservationID,
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)

	json.NewEncoder(w).Encode(response)
}

func validateCreateReservationRequest(req CreateReservationRequest) error {

	if req.WarehouseID <= 0 {
		return errors.New("invalid warehouse id")
	}

	if len(req.Items) == 0 {
		return errors.New("reservation items are required")
	}

	for _, item := range req.Items {

		if item.ProductID <= 0 {
			return errors.New("invalid product id")
		}

		if item.Quantity <= 0 {
			return errors.New("quantity must be greater than 0")
		}
	}

	return nil
}

func (h *Handler) checkWarehouse(
	ctx context.Context,
	tx *sql.Tx,
	warehouseID int64,
) error {

	var exists bool

	err := tx.QueryRowContext(
		ctx,
		`
		SELECT EXISTS(
			SELECT 1
			FROM warehouses
			WHERE id = $1
		)
		`,
		warehouseID,
	).Scan(&exists)

	if err != nil {
		return err
	}

	if !exists {
		return sql.ErrNoRows
	}

	return nil
}

func (h *Handler) checkStock(
	ctx context.Context,
	tx *sql.Tx,
	req CreateReservationRequest,
) error {

	for _, item := range req.Items {

		var stockQuantity int

		err := tx.QueryRowContext(
			ctx,
			`
			SELECT quantity
			FROM stock
			WHERE warehouse_id = $1
			  AND product_id = $2
			FOR UPDATE
			`,
			req.WarehouseID,
			item.ProductID,
		).Scan(&stockQuantity)

		if err != nil {
			return err
		}

		if stockQuantity < item.Quantity {
			return ErrInsufficientStock
		}
	}

	return nil
}

func (h *Handler) createReservation(
	ctx context.Context,
	tx *sql.Tx,
	warehouseID int64,
) (int64, error) {

	var reservationID int64

	err := tx.QueryRowContext(
		ctx,
		`
		INSERT INTO reservations (
			warehouse_id,
			status,
			expires_at
		)
		VALUES (
			$1,
			'active',
			NOW() + INTERVAL '15 minutes'
		)
		RETURNING id
		`,
		warehouseID,
	).Scan(&reservationID)

	if err != nil {
		return 0, err
	}

	return reservationID, nil
}

func (h *Handler) createReservationItems(
	ctx context.Context,
	tx *sql.Tx,
	reservationID int64,
	items []CreateReservationItemRequest,
) error {

	for _, item := range items {

		_, err := tx.ExecContext(
			ctx,
			`
			INSERT INTO reservation_items (
				reservation_id,
				product_id,
				quantity
			)
			VALUES ($1, $2, $3)
			`,
			reservationID,
			item.ProductID,
			item.Quantity,
		)

		if err != nil {
			return err
		}
	}

	return nil
}

func (h *Handler) decreaseStock(
	ctx context.Context,
	tx *sql.Tx,
	req CreateReservationRequest,
) error {

	for _, item := range req.Items {

		_, err := tx.ExecContext(
			ctx,
			`
			UPDATE stock
			SET quantity = quantity - $1,
			    updated_at = NOW()
			WHERE warehouse_id = $2
			  AND product_id = $3
			`,
			item.Quantity,
			req.WarehouseID,
			item.ProductID,
		)

		if err != nil {
			return err
		}
	}

	return nil
}

func (h *Handler) restoreReservationStock(
	ctx context.Context,
	tx *sql.Tx,
	reservationID int64,
	warehouseID int64,
) error {

	type ReservationItem struct {
		ProductID int64
		Quantity  int
	}

	var items []ReservationItem

	rows, err := tx.QueryContext(
		ctx,
		`
		SELECT product_id, quantity
		FROM reservation_items
		WHERE reservation_id = $1
		`,
		reservationID,
	)

	if err != nil {
		return err
	}

	for rows.Next() {
		var item ReservationItem

		if err := rows.Scan(&item.ProductID, &item.Quantity); err != nil {
			rows.Close()
			return err
		}

		items = append(items, item)
	}

	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}

	rows.Close()

	for _, item := range items {

		result, err := tx.ExecContext(
			ctx,
			`
			UPDATE stock
			SET quantity = quantity + $1,
			    updated_at = NOW()
			WHERE warehouse_id = $2
			  AND product_id = $3
			`,
			item.Quantity,
			warehouseID,
			item.ProductID,
		)

		if err != nil {
			return err
		}

		affected, err := result.RowsAffected()
		if err != nil {
			return err
		}

		if affected == 0 {
			return sql.ErrNoRows
		}
	}

	return nil
}

func (h *Handler) CancelReservation(w http.ResponseWriter, r *http.Request) {

	reservationID, err := strconv.ParseInt(
		r.PathValue("reservation_id"),
		10,
		64,
	)

	if err != nil {
		http.Error(w, "invalid reservation id", http.StatusBadRequest)
		return
	}

	tx, err := h.db.BeginTx(r.Context(), nil)
	if err != nil {
		http.Error(w, "failed to start transaction", http.StatusInternalServerError)
		return
	}

	defer tx.Rollback()

	var warehouseID int64
	var status string

	err = tx.QueryRowContext(
		r.Context(),
		`
		SELECT warehouse_id, status
		FROM reservations
		WHERE id = $1
		FOR UPDATE
		`,
		reservationID,
	).Scan(&warehouseID, &status)

	if err == sql.ErrNoRows {
		http.Error(w, "reservation not found", http.StatusNotFound)
		return
	}

	if err != nil {
		http.Error(w, "failed to get reservation", http.StatusInternalServerError)
		return
	}

	if status != "active" {
		http.Error(w, "reservation cannot be cancelled", http.StatusConflict)
		return
	}

	if err := h.restoreReservationStock(r.Context(), tx, reservationID, warehouseID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			http.Error(w, "stock not found", http.StatusNotFound)
			return
		}

		http.Error(w, "failed to restore stock: "+err.Error(), http.StatusInternalServerError)
		return
	}

	_, err = tx.ExecContext(
		r.Context(),
		`
		UPDATE reservations
		SET status = 'cancelled'
		WHERE id = $1
		`,
		reservationID,
	)

	if err != nil {
		http.Error(
			w,
			"failed to cancel reservation",
			http.StatusInternalServerError,
		)
		return
	}

	if err := tx.Commit(); err != nil {
		http.Error(
			w,
			"failed to commit transaction",
			http.StatusInternalServerError,
		)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) ConfirmReservation(w http.ResponseWriter, r *http.Request) {

	reservationID, err := strconv.ParseInt(
		r.PathValue("reservation_id"),
		10,
		64,
	)

	if err != nil {
		http.Error(w, "invalid reservation id", http.StatusBadRequest)
		return
	}

	tx, err := h.db.BeginTx(r.Context(), nil)
	if err != nil {
		http.Error(w, "failed to start transaction", http.StatusInternalServerError)
		return
	}

	defer tx.Rollback()

	var status string
	var warehouseID int64
	var expiresAt time.Time

	err = tx.QueryRowContext(
		r.Context(),
		`
		SELECT warehouse_id, status, expires_at
		FROM reservations
		WHERE id = $1
		FOR UPDATE
		`,
		reservationID,
	).Scan(&warehouseID, &status, &expiresAt)

	if err == sql.ErrNoRows {
		http.Error(w, "reservation not found", http.StatusNotFound)
		return
	}

	if err != nil {
		http.Error(w, "failed to get reservation", http.StatusInternalServerError)
		return
	}

	if status != "active" {
		http.Error(w, "reservation cannot be confirmed", http.StatusConflict)
		return
	}

	if !expiresAt.After(time.Now().UTC()) {
		if err := h.restoreReservationStock(r.Context(), tx, reservationID, warehouseID); err != nil {
			http.Error(w, "failed to restore stock", http.StatusInternalServerError)
			return
		}

		if _, err := tx.ExecContext(
			r.Context(),
			`UPDATE reservations SET status = 'cancelled' WHERE id = $1`,
			reservationID,
		); err != nil {
			http.Error(w, "failed to expire reservation", http.StatusInternalServerError)
			return
		}

		if err := tx.Commit(); err != nil {
			http.Error(w, "failed to commit transaction", http.StatusInternalServerError)
			return
		}

		http.Error(w, "reservation expired", http.StatusConflict)
		return
	}

	_, err = tx.ExecContext(
		r.Context(),
		`
		UPDATE reservations
		SET status = 'confirmed'
		WHERE id = $1
		`,
		reservationID,
	)

	if err != nil {
		http.Error(w, "failed to confirm reservation", http.StatusInternalServerError)
		return
	}

	if err := tx.Commit(); err != nil {
		http.Error(w, "failed to commit transaction", http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) ExpireReservations(ctx context.Context) error {

	log.Println("expire: starting")

	tx, err := h.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}

	defer tx.Rollback()

	log.Println("expire: transaction started")

	rows, err := tx.QueryContext(
		ctx,
		`
		SELECT id, warehouse_id
		FROM reservations
		WHERE status = 'active'
		  AND expires_at <= NOW()
		FOR UPDATE
		`,
	)

	if err != nil {
		return err
	}

	type ExpiredReservation struct {
		ID          int64
		WarehouseID int64
	}

	var reservations []ExpiredReservation

	for rows.Next() {

		var reservation ExpiredReservation

		if err := rows.Scan(
			&reservation.ID,
			&reservation.WarehouseID,
		); err != nil {
			rows.Close()
			return err
		}

		reservations = append(reservations, reservation)
	}

	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}

	rows.Close()

	log.Println(
		"expire: found",
		len(reservations),
		"expired reservations",
	)

	for _, reservation := range reservations {

		log.Println(
			"expire: processing reservation",
			reservation.ID,
		)

		itemRows, err := tx.QueryContext(
			ctx,
			`
			SELECT product_id, quantity
			FROM reservation_items
			WHERE reservation_id = $1
			`,
			reservation.ID,
		)

		if err != nil {
			return fmt.Errorf(
				"get reservation items: %w",
				err,
			)
		}

		type ReservationItem struct {
			ProductID int64
			Quantity  int
		}

		var items []ReservationItem

		for itemRows.Next() {

			var item ReservationItem

			if err := itemRows.Scan(
				&item.ProductID,
				&item.Quantity,
			); err != nil {
				itemRows.Close()
				return fmt.Errorf(
					"read reservation item: %w",
					err,
				)
			}

			items = append(items, item)
		}

		if err := itemRows.Err(); err != nil {
			itemRows.Close()
			return fmt.Errorf(
				"reservation items rows: %w",
				err,
			)
		}

		itemRows.Close()

		for _, item := range items {

			log.Printf(
				"expire: restoring product=%d quantity=%d",
				item.ProductID,
				item.Quantity,
			)

			_, err := tx.ExecContext(
				ctx,
				`
				UPDATE stock
				SET quantity = quantity + $1,
				    updated_at = NOW()
				WHERE warehouse_id = $2
				  AND product_id = $3
				`,
				item.Quantity,
				reservation.WarehouseID,
				item.ProductID,
			)

			if err != nil {
				return fmt.Errorf(
					"restore stock: %w",
					err,
				)
			}
		}

		_, err = tx.ExecContext(
			ctx,
			`
			UPDATE reservations
			SET status = 'cancelled'
			WHERE id = $1
			`,
			reservation.ID,
		)

		if err != nil {
			return fmt.Errorf(
				"cancel expired reservation: %w",
				err,
			)
		}
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf(
			"commit expire transaction: %w",
			err,
		)
	}

	log.Println("expire: completed")

	return nil
}
