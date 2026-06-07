.PHONY: help run build test clean tidy docker-up docker-down docker-logs

help:
	@echo "Available targets:"
	@echo "  make run          - run the API locally"
	@echo "  make build        - build the API binary"
	@echo "  make test         - run tests"
	@echo "  make clean        - clean build artifacts"
	@echo "  make tidy         - tidy Go modules"
	@echo "  make docker-up    - start full Docker stack"
	@echo "  make docker-down  - stop full Docker stack"
	@echo "  make docker-logs  - tail Docker logs"

run:
	go run cmd/api/main.go

build:
	go build -o bin/api cmd/api/main.go

test:
	go test -v ./...

clean:
	rm -rf bin/
	rm -rf logs/
	go clean

tidy:
	go mod tidy
	go mod download

docker-up:
	docker compose up -d --build

docker-down:
	docker compose down

docker-logs:
	docker compose logs -f --tail=200
