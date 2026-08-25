CREATE TABLE reservation_items (
    id SERIAL PRIMARY KEY,
    reservation_id INT NOT NULL,
    product_id INT NOT NULL,
    quantity INT NOT NULL,

    FOREIGN KEY (reservation_id) REFERENCES reservations(id),
    FOREIGN KEY (product_id) REFERENCES products(id),

    CHECK (quantity > 0),
    UNIQUE (reservation_id, product_id)
);