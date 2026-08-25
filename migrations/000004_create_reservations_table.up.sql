CREATE TABLE reservations (
    id SERIAL PRIMARY KEY,
    warehouse_id INT NOT NULL,
    status VARCHAR(20) NOT NULL,

    created_at TIMESTAMP DEFAULT NOW(),
    expires_at TIMESTAMP NOT NULL,

    CHECK (status IN ('active', 'confirmed', 'cancelled')),
    FOREIGN KEY (warehouse_id) REFERENCES warehouses(id)
);