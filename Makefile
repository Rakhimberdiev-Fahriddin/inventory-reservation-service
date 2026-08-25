DB_URL = postgres://postgres:1@localhost:5432/inventory_db?sslmode=disable

mg-gen:
	migrate create -ext sql -dir ./migrations -seq $(name)

mg-up:
	migrate -path ./migrations -database ${DB_URL} up

mg-down:
	migrate -path ./migrations -database ${DB_URL} down 1

mg-force:
	migrate -path ./migrations -database ${DB_URL} force $(version)