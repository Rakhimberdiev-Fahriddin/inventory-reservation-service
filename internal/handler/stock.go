package handler

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"strconv"
)

type AddStockRequest struct {
	ProductID int64 `json:"product_id"`
	Quantity  int   `json:"quantity"`
}

type StockResponse struct {
	ProductID      int64 `json:"product_id"`
	WarehouseID    int64 `json:"warehouse_id"`
	PhysicalStock  int   `json:"physical_stock"`
	AvailableStock int   `json:"available_stock"`
}

type Handler struct {
	db *sql.DB
}

func NewHandler(db *sql.DB) *Handler {
	return &Handler{
		db: db,
	}
}

func (h *Handler) AddStock(w http.ResponseWriter, r *http.Request) {
	warehouseID, err := strconv.ParseInt(r.PathValue("warehouse_id"), 10, 64)
	if err != nil {
		http.Error(w, "invalid warehouse id", http.StatusBadRequest)
		return
	}

	var req AddStockRequest

	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}

	if req.Quantity <= 0 {
		http.Error(w, "quantity must be greater than 0", http.StatusBadRequest)
		return
	}

	result, err := h.db.ExecContext(
		r.Context(),
		`
		UPDATE stock
		SET quantity = quantity + $1,
		    updated_at = NOW()
		WHERE warehouse_id = $2
		  AND product_id = $3
		`,
		req.Quantity,
		warehouseID,
		req.ProductID,
	)
	if err != nil {
		http.Error(w, "failed to update stock", http.StatusInternalServerError)
		return
	}

	rows, err := result.RowsAffected()
	if err != nil {
		http.Error(w, "failed to check stock", http.StatusInternalServerError)
		return
	}

	if rows == 0 {
		http.Error(w, "stock not found", http.StatusNotFound)
		return
	}
}

func (h *Handler) GetStock(w http.ResponseWriter, r *http.Request) {
	warehouseID, err := strconv.ParseInt(r.PathValue("warehouse_id"), 10, 64)
	if err != nil {
		http.Error(w, "invalid warehouse id", http.StatusBadRequest)
		return
	}

	productID, err := strconv.ParseInt(r.PathValue("product_id"), 10, 64)
	if err != nil {
		http.Error(w, "invalid product id", http.StatusBadRequest)
		return
	}

	var quantity int

	err = h.db.QueryRowContext(
		r.Context(),
		`
	SELECT quantity
	FROM stock
	WHERE warehouse_id = $1
	  AND product_id = $2
	`,
		warehouseID,
		productID,
	).Scan(&quantity)

	if err == sql.ErrNoRows {
		http.Error(w, "stock not found", http.StatusNotFound)
		return
	}
	if err != nil {
		http.Error(w, "failed to get stock", http.StatusInternalServerError)
		return
	}

	responce := StockResponse{
		ProductID:      productID,
		WarehouseID:    warehouseID,
		PhysicalStock:  quantity,
		AvailableStock: quantity,
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	if err := json.NewEncoder(w).Encode(responce); err != nil {
		return
	}
}
