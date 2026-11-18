.PHONY: all build test vet fmt lint vuln clean docker-all docker-test docker-vet docker-fmt docker-lint docker-vuln docker-shell up down

all: fmt vet test build

build:
	go build -o bin/ ./cmd/... ./examples/city/backend

test:
	go test -race -count=1 ./...

vet:
	go vet ./...

fmt:
	@test -z "$$(gofmt -l .)" || (echo "gofmt needed:"; gofmt -l .; exit 1)

lint:
	golangci-lint run

vuln:
	govulncheck ./...

clean:
	rm -rf bin

# Containerized workflow (preferred). Nothing needs to be installed on the host.
DC = docker compose run --rm dev

docker-all:
	$(DC) make all

docker-test:
	$(DC) make test

docker-vet:
	$(DC) make vet

docker-fmt:
	$(DC) make fmt

docker-lint:
	$(DC) make lint

docker-vuln:
	$(DC) make vuln

docker-shell:
	$(DC) bash

up:
	docker compose up --build

down:
	docker compose down
