CREATE TABLE idempotency_keys (
    id SERIAL PRIMARY KEY,
    idempotency_key VARCHAR(255) NOT NULL UNIQUE,
    reservation_id INT NOT NULL,

    created_at TIMESTAMP DEFAULT NOW(),

    FOREIGN KEY (reservation_id) REFERENCES reservations(id)
);