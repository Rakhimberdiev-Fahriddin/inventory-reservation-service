package main

import (
	"context"
	"database/sql"
	"log"
	"net/http"
	"os"
	"time"

	"github.com/Rakhimberdiev-Fahriddin/inventory-reservation-service/internal/handler"
	_ "github.com/jackc/pgx/v5/stdlib"
)

const defaultDBURL = "postgres://postgres:1@localhost:5432/inventory_db?sslmode=disable"

func main() {
	dbURL := os.Getenv("DATABASE_URL")
	if dbURL == "" {
		dbURL = defaultDBURL
	}

	db, err := sql.Open("pgx", dbURL)
	if err != nil {
		log.Fatal(err)
	}
	defer db.Close()

	if err := db.Ping(); err != nil {
		log.Fatal(err)
	}

	log.Println("database connected")

	h := handler.NewHandler(db)

	mux := http.NewServeMux()

	mux.HandleFunc("POST /warehouses/{warehouse_id}/stock", h.AddStock)
	mux.HandleFunc("GET /warehouses/{warehouse_id}/products/{product_id}/stock", h.GetStock)
	mux.HandleFunc("POST /reservations", h.CreateReservation)
	mux.HandleFunc("GET /reservations/{reservation_id}", h.GetReservation)
	mux.HandleFunc("POST /reservations/{reservation_id}/cancel", h.CancelReservation)
	mux.HandleFunc("POST /reservations/{reservation_id}/confirm", h.ConfirmReservation)
	server := &http.Server{
		Addr:    ":8080",
		Handler: mux,
	}

	go func() {

		ticker := time.NewTicker(1 * time.Minute)
		defer ticker.Stop()

		for range ticker.C {

			if err := h.ExpireReservations(context.Background()); err != nil {
				log.Println("failed to expire reservations:", err)
			}
		}

	}()

	log.Println("server started on :8080")

	if err := server.ListenAndServe(); err != nil {
		log.Fatal(err)
	}
}
