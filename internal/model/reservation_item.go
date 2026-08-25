package model

type ReservationItem struct {
	ID            int64 `json:"id"`
	ReservationID int64 `json:"reservation_id"`
	ProductID     int64 `json:"product_id"`
	Quantity      int   `json:"quantity"`
}