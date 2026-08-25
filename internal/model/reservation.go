package model

import "time"

type Reservation struct {
	ID          int64      `json:"id"`
	WarehouseID int64      `json:"warehouse_id"`
	Status      string     `json:"status"`
	CreatedAt   time.Time  `json:"created_at"`
	ExpiresAt   *time.Time `json:"expires_at"`
}