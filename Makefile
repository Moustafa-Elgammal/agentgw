.PHONY: oidc-keycloak-check guides-check guides-lint site-build site-check site-dev all build test vet fmt lint vuln clean docker-all docker-test docker-vet docker-fmt docker-lint docker-vuln docker-shell up down helm-check deploy-check release-check release-snapshot workflows-check licenses docs-gen site-lint site-spell site-a11y site-dashes docs-quality

all: fmt vet test build

# Regenerate docs/reference from the code. A test fails when the committed pages are stale.
docs-gen:
	go run ./cmd/docgen

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

# Project site (Astro Starlight), in a container: Node is never installed on the host.
site-build:
	docker compose run --rm site build

site-check:
	docker compose run --rm site check

site-dev:
	docker compose run --rm --service-ports site dev

# Guides and tutorials. guides-lint checks the Markdown (tutorial pairs, links, manifest blocks,
# troubleshooting coverage) in the dev container. guides-check runs every tutorial script against
# the compose stack, so it needs Docker with Compose v2 and bash on the host, nothing else.
guides-lint:
	docker compose run --rm dev go test -count=1 ./test/docs/...

oidc-keycloak-check:
	bash scripts/oidc-keycloak-check.sh

guides-check:
	bash docs/tutorials/run-all.sh

# Documentation quality gates, all in containers (see AGENTS.md). docs-quality runs every one.
site-lint:
	docker compose run --rm markdownlint

site-spell:
	docker compose run --rm codespell

# Build the site, then audit the built pages: axe WCAG 2 A and AA, no horizontal scroll, no
# console errors, alt text, heading order, in light and dark at 1280 and 390 px.
site-a11y:
	docker compose run --rm site build
	docker compose run --rm site-audit

# The project forbids em dashes in prose. Generated and synced copies are not checked.
site-dashes:
	@if grep -rIn --exclude-dir=node_modules --exclude-dir=.git --exclude-dir=dist --exclude-dir=.astro --exclude-dir=worktrees --exclude-dir=soak-results --exclude-dir=bench-results --exclude-dir=out \
	  -e '—' -e '―' README.md CHANGELOG.md CONTRIBUTING.md SECURITY.md CODE_OF_CONDUCT.md AGENTS.md docs site/src/components site/src/styles site/scripts site/audit examples scripts; then \
	  echo "site-dashes: em dashes found above, use commas, colons or full stops"; exit 1; fi

docs-quality: site-dashes site-lint site-spell site-check site-a11y
