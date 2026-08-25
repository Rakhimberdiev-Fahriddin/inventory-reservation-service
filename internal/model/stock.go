package model

import "time"

type Stock struct {
	ID          int64     `json:"id"`
	WarehouseID int64     `json:"warehouse_id"`
	ProductID   int64     `json:"product_id"`
	Quantity    int       `json:"quantity"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}