.PHONY: all build test vet fmt lint vuln clean docker-all docker-test docker-vet docker-fmt docker-lint docker-vuln docker-shell up down helm-check deploy-check release-check release-snapshot workflows-check licenses

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

# Chart, alert rules and dashboard checks, in containers (no host install).
HELM = docker compose run --rm helm
HELM_CHART = /deploy/helm/agentgw

helm-check:
	$(HELM) lint $(HELM_CHART) -f $(HELM_CHART)/ci/default-values.yaml
	$(HELM) lint $(HELM_CHART) -f $(HELM_CHART)/ci/all-values.yaml
	$(HELM) template r $(HELM_CHART) -f $(HELM_CHART)/ci/default-values.yaml > /dev/null
	$(HELM) template r $(HELM_CHART) -f $(HELM_CHART)/ci/all-values.yaml > /dev/null

deploy-check: helm-check
	docker compose run --rm promtool check rules /deploy/prometheus/alerts.yaml
	docker compose run --rm dev python3 -m json.tool deploy/grafana/agentgw.json > /dev/null

licenses:
	docker compose run --rm licenses

# Release and workflow checks, in containers (no host install). dist/ is gitignored.
release-check:
	docker compose run --rm release check

release-snapshot:
	docker compose run --rm release release --snapshot --clean --skip=publish,sign,sbom

workflows-check:
	docker compose run --rm actionlint
